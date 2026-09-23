package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func TestReplyCheckHoldsBackOneReplyPerExchange(t *testing.T) {
	c := config()
	var calls, checks atomic.Int32
	var sawNotice atomic.Bool
	c.Spec.Provider = streamFunc(func(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
		n := calls.Add(1)
		for _, m := range r.Messages {
			if m.Role == "user" && strings.Contains(m.Content.Text(), "step-2 is open") {
				sawNotice.Store(true)
			}
		}
		if n == 1 {
			return provider.Response{Content: "all steps complete"}, nil
		}
		return provider.Response{Content: "step 1 is complete; step 2 remains open"}, nil
	})
	// The check objects every time; only the first reply of the exchange is held back.
	c.Spec.ReplyCheck = func(_ context.Context, self message.ActorID) string {
		checks.Add(1)
		if self != c.ID {
			t.Errorf("check got %q, want the agent's own id", self)
		}
		return "Before you reply: step-2 is open."
	}
	replies := make(chan message.Draft, 2)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	if reply := await(t, replies); reply.Content != "step 1 is complete; step 2 remains open" {
		t.Fatalf("sent %q", reply.Content)
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 2 || checks.Load() != 1 || !sawNotice.Load() {
		t.Fatalf("calls %d checks %d saw notice %v", calls.Load(), checks.Load(), sawNotice.Load())
	}
	select {
	case extra := <-replies:
		t.Fatalf("the held-back draft was sent too: %q", extra.Content)
	default:
	}
}
