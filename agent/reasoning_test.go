package agent_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func TestReasoningFailureRetainsOrderedChannelsWithoutHistory(t *testing.T) {
	for _, mode := range []string{"upstream failure", "prefix conflict", "invalid channel"} {
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
			if finished.Status != agent.OutputFailed || finished.HistoryPosition != nil || finished.Bytes != 6 || finished.ReasoningBytes != uint64(len("why 🌎 next")) {
				t.Fatal(finished)
			}
		})
	}
}
