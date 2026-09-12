package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func TestCommentaryPrecedesToolsWithoutAddingHistory(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		observe    bool
	}{
		{"text", "  Let me inspect the files.\n", true},
		{"empty", "", true},
		{"whitespace", " \n\t", true},
		{"no observer", "Let me inspect the files.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var observed []string
			if tc.observe {
				c.OnCommentary = func(text string) { observed = append(observed, "commentary:"+text) }
			}
			c.OnTool = func(activity agent.ToolActivity) {
				if activity.FinishedAt.IsZero() {
					observed = append(observed, "tool:"+activity.Call.ID)
				}
			}
			c.Spec.Tools = []tool.Tool{customTool{name: "read"}}
			calls := 0
			c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
				calls++
				if calls == 1 {
					return provider.Response{Content: tc.text, ToolCalls: []provider.ToolCall{
						{ID: "one", Name: "read"}, {ID: "two", Name: "read"},
					}}, nil
				}
				if len(r.Messages) != 5 {
					t.Fatalf("unexpected history: %+v", r.Messages)
				}
				assistant := r.Messages[2]
				if assistant.Role != "assistant" || assistant.Content.Text() != tc.text || len(assistant.ToolCalls) != 2 || assistant.Envelope != nil {
					t.Fatalf("assistant history changed: %+v", assistant)
				}
				return provider.Response{Content: "done"}, nil
			})
			c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
				if d.Kind != message.Reply || d.Content != "done" || d.ReplyTo != "input" {
					t.Fatalf("unexpected routed message: %+v", d)
				}
				observed = append(observed, "reply")
				cancel()
				return message.Receipt{}, nil
			})
			c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "inspect"})
			if err := mustAgent(t, c).Run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			want := []string{"tool:one", "tool:two", "reply"}
			if tc.name == "text" {
				want = append([]string{"commentary:" + tc.text}, want...)
			}
			if calls != 2 || !reflect.DeepEqual(observed, want) {
				t.Fatalf("calls=%d events=%q, want %q", calls, observed, want)
			}
		})
	}
}

func TestRejectedResponsesDoNotEmitCommentary(t *testing.T) {
	for _, mode := range []string{"provider error", "cancel", "stop"} {
		t.Run(mode, func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.OnCommentary = func(string) { t.Fatal("rejected response emitted commentary") }
			c.OnTool = func(agent.ToolActivity) { t.Fatal("rejected response executed tools") }
			var a *agent.Agent
			c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
				response := provider.Response{Content: "Checking files.", ToolCalls: []provider.ToolCall{{ID: "one", Name: "read"}}}
				switch mode {
				case "provider error":
					return response, errors.New("rejected")
				case "cancel":
					cancel()
				case "stop":
					a.RequestStop()
				}
				return response, nil
			})
			a = mustAgent(t, c)
			c.Inbox.Send(message.Message{Kind: message.Instruction, Content: "inspect"})
			if err := a.Run(ctx); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}
