package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// Wake context is appended once per exchange, after every queued input and
// before the first model call, and never again during the exchange's tool loop.
func TestWakeContextIsAppendedOncePerExchange(t *testing.T) {
	c := config()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drafts := make(chan message.Draft, 2)
	sent := 0
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		sent++
		drafts <- d
		if sent == 2 {
			cancel()
		}
		return message.Receipt{}, nil
	})
	c.Spec.Tools = []tool.Tool{customTool{name: "read"}}
	wakes := 0
	c.WakeContext = func(_ context.Context, inputs []message.Message) (*message.Message, error) {
		wakes++
		ids := make([]string, len(inputs))
		for i, m := range inputs {
			ids[i] = string(m.ID)
		}
		state := work.ActorState{Assigned: []work.WorkState{{WorkID: work.ID("work-" + strings.Join(ids, "+")), Revision: work.Revision(wakes)}}}
		return &message.Message{Kind: message.Observation, State: &state}, nil
	}
	calls := 0
	c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
		calls++
		last := r.Messages[len(r.Messages)-1]
		switch calls {
		case 1:
			// system, input, second input, wake context
			if len(r.Messages) != 4 || last.Role != "user" || last.Envelope == nil || last.Envelope.State == nil || last.Envelope.Kind != message.Observation || last.Envelope.ID != "worker/wake-1" {
				t.Errorf("first exchange: %+v", r.Messages)
			}
			if got := last.Envelope.State.Assigned[0].WorkID; got != "work-first+second" {
				t.Errorf("wake context did not see every input: %s", got)
			}
			var decoded message.Message
			if err := json.Unmarshal([]byte(last.Content.Text()), &decoded); err != nil || decoded.State == nil || decoded.State.Assigned[0].Revision != 1 {
				t.Errorf("state is not in the model-visible text: %s", last.Content.Text())
			}
			return provider.Response{ToolCalls: []provider.ToolCall{{ID: "one", Name: "read", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
		case 2:
			// The tool loop continues the same exchange: no second wake context.
			if last.Role != "tool" || wakes != 1 {
				t.Errorf("tool iteration re-added wake context: wakes=%d last=%+v", wakes, last)
			}
			return provider.Response{Content: "first answer"}, nil
		case 3:
			if wakes != 2 || last.Envelope == nil || last.Envelope.ID != "worker/wake-2" || last.Envelope.State.Assigned[0].WorkID != "work-third" {
				t.Errorf("second exchange: wakes=%d last=%+v", wakes, last)
			}
			return provider.Response{Content: "second answer"}, nil
		}
		return provider.Response{}, errors.New("unexpected call")
	})
	a := mustAgent(t, c)
	c.Inbox.Send(message.Message{ID: "first", Kind: message.Instruction, Content: "go"})
	c.Inbox.Send(message.Message{ID: "second", Kind: message.Notification, Content: "note"})
	go func() {
		<-drafts
		c.Inbox.Send(message.Message{ID: "third", Kind: message.Instruction, Content: "again"})
	}()
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 3 || wakes != 2 {
		t.Fatal(calls, wakes)
	}
}

func TestWakeContextNilAddsNothingAndErrorsAreFatal(t *testing.T) {
	c := config()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Outbox = senderFunc(func(context.Context, message.Draft) (message.Receipt, error) { cancel(); return message.Receipt{}, nil })
	c.WakeContext = func(context.Context, []message.Message) (*message.Message, error) { return nil, nil }
	c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
		if len(r.Messages) != 2 {
			t.Errorf("nil wake context changed history: %+v", r.Messages)
		}
		return provider.Response{Content: "ok"}, nil
	})
	a := mustAgent(t, c)
	c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "go"})
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	c = config()
	c.WakeContext = func(context.Context, []message.Message) (*message.Message, error) {
		return nil, errors.New("store unavailable")
	}
	a = mustAgent(t, c)
	c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "go"})
	if err := a.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "wake context: store unavailable") || a.State() != agent.Failed {
		t.Fatal(err, a.State())
	}
}
