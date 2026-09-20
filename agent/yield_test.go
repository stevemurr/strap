package agent_test

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"sync/atomic"
	"testing"
)

type countedTool struct{ count *atomic.Int32 }

func (t countedTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "effect", Parameters: t.InputContract().Schema()}
}
func (t countedTool) Call(context.Context, tool.Call) (tool.Result, error) {
	t.count.Add(1)
	return tool.Text("effect"), nil
}
func TestYieldSettlesAndResumesQueuedInputWithoutReply(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "after-yield", true: "before-yield"}[queued], func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			yielded := make(chan agent.Yielded, 1)
			replies := make(chan message.Draft, 2)
			var calls atomic.Int32
			c.Spec.Tools = []tool.Tool{tool.WaitForInput()}
			c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
				if y, ok := e.(agent.Yielded); ok {
					yielded <- y
				}
				return nil
			})
			help := func() { c.Inbox.Send(message.Message{ID: "help", Kind: message.Instruction, Content: "continue"}) }
			c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
				if calls.Add(1) == 1 {
					if queued {
						help()
					}
					return provider.Response{Content: "Waiting for help.", ToolCalls: []provider.ToolCall{{ID: "wait", Name: "wait_for_input", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
				}
				return provider.Response{Content: "resumed"}, nil
			})
			c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
				replies <- d
				return message.Receipt{}, nil
			})
			c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
			a := mustAgent(t, c)
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			y := await(t, yielded)
			if y.CallID != "wait" || y.SettledRevision != 4 {
				t.Fatal(y)
			}
			if !queued {
				if calls.Load() != 1 {
					t.Fatal("synthetic generation")
				}
				help()
			}
			reply := await(t, replies)
			if reply.Content != "resumed" || reply.ReplyTo != "help" || calls.Load() != 2 {
				t.Fatal(reply, calls.Load())
			}
			cancel()
			await(t, done)
		})
	}
}
func TestMixedControlBatchHasNoEffectsAndContinues(t *testing.T) {
	c := config()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var effects, calls atomic.Int32
	replies := make(chan message.Draft, 1)
	c.Spec.Tools = []tool.Tool{countedTool{&effects}, tool.WaitForInput()}
	c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
		if calls.Add(1) == 1 {
			return provider.Response{ToolCalls: []provider.ToolCall{{ID: "effect", Name: "effect", Arguments: json.RawMessage(`{"input":{}}`)}, {ID: "wait", Name: "wait_for_input", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
		}
		n := len(r.Messages)
		if r.Messages[n-2].ToolCallID != "effect" || r.Messages[n-1].ToolCallID != "wait" {
			t.Error("unmatched results")
		}
		return provider.Response{Content: "corrected"}, nil
	})
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	a := mustAgent(t, c)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	await(t, replies)
	if effects.Load() != 0 || calls.Load() != 2 {
		t.Fatal(effects.Load(), calls.Load())
	}
	cancel()
	await(t, done)
}

func (t countedTool) InputContract() tool.Contract {
	p, err := tool.NewParameters[struct{}]()
	if err != nil {
		panic(err)
	}
	return p.Contract()
}
