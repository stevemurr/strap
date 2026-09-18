package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// silent reports a stalled stream for the first n calls, then answers. A stall
// commits nothing and the model never learns of it, so the repeat is the same
// request rather than a correction.
func silent(n int, calls *atomic.Int32) streamFunc {
	return func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
		if int(calls.Add(1)) > n {
			return provider.Response{Content: "acted"}, nil
		}
		return provider.Response{}, provider.ErrStreamStalled
	}
}

func TestStalledCallRepeatsWithoutTellingTheModel(t *testing.T) {
	c := config()
	var calls atomic.Int32
	c.Spec.Provider = silent(2, &calls)
	var appended atomic.Int32
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		// The delivered instruction is the only user message a clean exchange
		// appends; a retry notice would be a second one.
		if v, ok := e.(agent.HistoryAppended); ok && v.Message.Role == "user" {
			appended.Add(1)
		}
		return nil
	})
	replies := make(chan message.Draft, 1)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	if reply := await(t, replies); reply.Content != "acted" {
		t.Fatal(reply)
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("two stalls should cost three attempts, got %d", calls.Load())
	}
	if appended.Load() != 1 {
		t.Fatalf("history gained %d user messages; a stall must add none, since the model never saw it", appended.Load())
	}
}

// The repeats are bounded: a server that never produces output ends the
// exchange rather than looping on it.
func TestPersistentStallEndsTheExchange(t *testing.T) {
	c := config()
	var calls atomic.Int32
	c.Spec.Provider = silent(99, &calls)
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	err := mustAgent(t, c).Run(context.Background())
	if !errors.Is(err, provider.ErrStreamStalled) {
		t.Fatalf("unexpected end: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("attempts = %d, want the original plus two repeats", calls.Load())
	}
}
