package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
)

func collectExits(t *testing.T, c *conversation.Controller) func() []error {
	t.Helper()
	var exits []error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := c.NextEvent(context.Background())
			if err != nil {
				return
			}
			if x, ok := e.(conversation.AgentExited); ok {
				exits = append(exits, x.Err)
			}
		}
	}()
	return func() []error {
		<-done
		return exits
	}
}

// Shutdown is a request before it is a cancellation, so an agent that is idle
// when the host closes exits reporting nothing. Consumers that once had to
// filter context.Canceled to tell routine shutdown from failure no longer see
// one at all.
func TestCloseLetsIdleAgentsExitWithoutCancellation(t *testing.T) {
	c, _ := setup(t)
	exits := collectExits(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, err := range exits() {
		if err != nil {
			t.Errorf("clean shutdown reported %v", err)
		}
	}
	if _, err := c.Send(c.Root(), "late"); !errors.Is(err, conversation.ErrClosed) {
		t.Errorf("send after a settled close: %v", err)
	}
}

// A request that cannot settle still has to end. The wait escalates to
// cancellation, the loops unwind, and the exit keeps the cancellation because
// that work really was aborted.
func TestCloseEscalatesWhenAnAgentCannotSettle(t *testing.T) {
	c, m := setup(t)
	exits := collectExits(t, c)
	if _, err := c.Send(c.Root(), "work"); err != nil {
		t.Fatal(err)
	}
	m.next(t) // The root is now blocked in a model call that never answers.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	cancelled := false
	for _, err := range exits() {
		cancelled = cancelled || errors.Is(err, context.Canceled)
	}
	if !cancelled {
		t.Error("an aborted agent must report its cancellation")
	}
	for _, a := range c.Agents() {
		if !a.State.Terminal() {
			t.Errorf("unjoined agent: %+v", a)
		}
	}
}
