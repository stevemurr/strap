package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"sync/atomic"
	"testing"
)

func TestNewExchangeAdmissionAndUnconditionalToolContinuation(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale-only", true: "mixed-and-tool-error"}[mixed], func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var admissions, calls atomic.Int32
			dispositions := make(chan agent.InboxDisposition, 4)
			replies := make(chan message.Draft, 1)
			c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
				if d, ok := e.(agent.InboxDisposition); ok {
					dispositions <- d
				}
				return nil
			})
			c.AdmitInbox = func(_ context.Context, ms []message.Message) (agent.InboxDecision, error) {
				admissions.Add(1)
				wake := false
				for _, m := range ms {
					wake = wake || m.Kind == message.Instruction
				}
				return agent.InboxDecision{Wake: wake}, nil
			}
			c.Spec.Tools = []tool.Tool{customTool{name: "fail", err: errors.New("assignment cancelled")}}
			c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
				if calls.Add(1) == 1 {
					return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "fail", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
				}
				if r.Messages[len(r.Messages)-1].Role != "tool" {
					t.Error("missing tool continuation")
				}
				return provider.Response{Content: "handled cancellation"}, nil
			})
			c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
				replies <- d
				return message.Receipt{}, nil
			})
			c.Inbox.Send(message.Message{ID: "stale", Kind: message.Notification, Content: "covered"})
			if mixed {
				c.Inbox.Send(message.Message{ID: "user", Kind: message.Instruction, Content: "steering"})
			}
			a := mustAgent(t, c)
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			d := await(t, dispositions)
			if d.Decision.Wake != mixed {
				t.Fatal(d)
			}
			if mixed {
				reply := await(t, replies)
				if reply.ReplyTo != "user" || calls.Load() != 2 || admissions.Load() != 1 {
					t.Fatal(reply, calls.Load(), admissions.Load())
				}
			} else if calls.Load() != 0 {
				t.Fatal("stale notice generated")
			}
			cancel()
			await(t, done)
		})
	}
}
