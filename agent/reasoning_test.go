package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func TestSuccessfulReasoningSurvivesToolFollowup(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "streamed"}[streaming], func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Spec.Tools = []tool.Tool{customTool{name: "read"}}
			calls := 0
			c.Spec.Provider = streamFunc(func(_ context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
				calls++
				if calls == 1 {
					if streaming {
						if err := o.OnDelta(provider.Delta{Channel: provider.ChannelReasoning, Text: "inspect 🌎"}); err != nil {
							return provider.Response{}, err
						}
					}
					return provider.Response{Reasoning: "inspect 🌎 first", ToolCalls: []provider.ToolCall{{ID: "one", Name: "read", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
				}
				if len(r.Messages) != 4 {
					t.Fatalf("unexpected history: %+v", r.Messages)
				}
				assistant, result := r.Messages[2], r.Messages[3]
				if assistant.Role != "assistant" || assistant.Reasoning != "inspect 🌎 first" || assistant.Content.Text() != "" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "one" {
					t.Fatalf("lost tool-call reasoning: %+v", assistant)
				}
				if result.Role != "tool" || result.ToolCallID != "one" || result.Content.Text() != "result" || result.Reasoning != "" {
					t.Fatalf("changed tool result: %+v", result)
				}
				return provider.Response{Reasoning: "finished checking", Content: "done"}, nil
			})
			c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
				if d.Content != "done" {
					t.Fatalf("reasoning entered routed answer: %+v", d)
				}
				cancel()
				return message.Receipt{}, nil
			})
			c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "inspect"})
			a := mustAgent(t, c)
			if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			page, err := a.Transcript(agent.TranscriptQuery{Limit: 100})
			if err != nil || calls != 2 || len(page.Entries) != 5 {
				t.Fatal(page, calls, err)
			}
			if last := page.Entries[4].Message; last.Reasoning != "finished checking" || last.Content.Text() != "done" {
				t.Fatalf("final response lost reasoning: %+v", last)
			}
		})
	}
}

func TestReasoningFailureRetainsOrderedChannelsWithoutHistory(t *testing.T) {
	for _, mode := range []string{"upstream failure", "canceled", "prefix conflict", "invalid channel"} {
		t.Run(mode, func(t *testing.T) {
			c := config()
			var mu sync.Mutex
			var deltas []agent.OutputDelta
			var finished agent.OutputFinished
			c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
				mu.Lock()
				defer mu.Unlock()
				switch v := e.(type) {
				case agent.OutputDelta:
					deltas = append(deltas, v)
				case agent.OutputFinished:
					finished = v
				}
				return nil
			})
			c.Spec.Provider = streamFunc(func(_ context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
				for _, d := range []provider.Delta{{Channel: provider.ChannelReasoning, Text: "why 🌎"}, {Text: "answer"}, {Channel: provider.ChannelReasoning, Text: " next"}} {
					if err := o.OnDelta(d); err != nil {
						return provider.Response{}, err
					}
				}
				switch mode {
				case "upstream failure":
					return provider.Response{}, errors.New("length limit")
				case "canceled":
					return provider.Response{Content: "answer", Reasoning: "why 🌎 next"}, context.Canceled
				case "prefix conflict":
					return provider.Response{Content: "answer unobserved", Reasoning: "conflict"}, nil
				default:
					_ = o.OnDelta(provider.Delta{Channel: "unknown", Text: "bad"})
					return provider.Response{Content: "answer", Reasoning: "why 🌎 next"}, nil
				}
			})
			a := mustAgent(t, c)
			_ = c.Inbox.Send(message.Message{ID: "input", From: message.User, To: c.ID, Content: "go"})
			if err := a.Run(context.Background()); err == nil {
				t.Fatal("accepted invalid completion")
			}
			page, err := a.Transcript(agent.TranscriptQuery{Limit: 100})
			if err != nil || len(page.Entries) != 2 {
				t.Fatal(page, err)
			}
			mu.Lock()
			defer mu.Unlock()
			var order []provider.OutputChannel
			texts := map[provider.OutputChannel]string{}
			for _, d := range deltas {
				if d.Offset != uint64(len(texts[d.Channel])) {
					t.Fatal(d)
				}
				texts[d.Channel] += d.Text
				if len(order) == 0 || order[len(order)-1] != d.Channel {
					order = append(order, d.Channel)
				}
			}
			if !reflect.DeepEqual(order, []provider.OutputChannel{provider.ChannelReasoning, provider.ChannelContent, provider.ChannelReasoning}) || texts[provider.ChannelContent] != "answer" || texts[provider.ChannelReasoning] != "why 🌎 next" {
				t.Fatal(order, texts)
			}
			wantStatus := agent.OutputFailed
			if mode == "canceled" {
				wantStatus = agent.OutputCanceled
			}
			if finished.Status != wantStatus || finished.HistoryPosition != nil || finished.Bytes != 6 || finished.ReasoningBytes != uint64(len("why 🌎 next")) {
				t.Fatal(finished)
			}
		})
	}
}
