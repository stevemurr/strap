package conversation

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
)

type interruptAttempt struct {
	done chan struct{}
	err  error
}

// Interrupted reports the routing/creation fence, including settlement. This
// remains true until a valid user delivery is admitted, independently of whether
// individual agents have published their acknowledged interrupted state.
func (c *Controller) Interrupted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interrupted
}

// Interrupt fences creation and routing, cancels every live agent's exchange,
// and discards queued input with explicit undelivered receipts. The caller's
// context limits the wait, not cancellation ownership. Only a new Send releases
// the settled agents; ResumeAgent cannot continue interrupted execution.
func (c *Controller) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closedLocked() {
		c.mu.Unlock()
		return ErrClosed
	}
	a := c.interruption
	if a == nil {
		c.interrupted = true
		a = &interruptAttempt{done: make(chan struct{})}
		c.interruption = a
		var agents []*ownedAgent
		for _, id := range c.order {
			agents = append(agents, c.agents[id])
		}
		// Request cancellation before returning control to callers. These requests
		// take only the agents' short control lock, never their publication lock.
		var waits []<-chan struct{}
		for _, owned := range agents {
			waits = append(waits, owned.agent.RequestInterrupt())
		}
		go c.finishInterruption(a, agents, waits)
	}
	c.mu.Unlock()
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) finishInterruption(a *interruptAttempt, agents []*ownedAgent, waits []<-chan struct{}) {
	for _, done := range waits {
		<-done
	}
	// Join any delivery that crossed admission before the interruption fence.
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, owned := range agents {
		for _, m := range owned.inbox.Drain() {
			r := message.Receipt{MessageID: m.ID, Recipient: m.To, Status: message.Undelivered,
				Detail: "User interrupted execution before this message was consumed"}
			c.receipts[m.ID] = r
			a.err = errors.Join(a.err, c.emitLocked(AckEvent{Receipt: r}))
		}
		if owned.info.ID == c.root && owned.agent.State().Terminal() {
			a.err = errors.Join(a.err, ErrAgentStopped)
		}
	}
	if c.closedLocked() {
		a.err = errors.Join(a.err, ErrClosed)
	}
	close(a.done)
}

func (c *Controller) interruptionPendingLocked() bool {
	if c.interruption == nil {
		return false
	}
	select {
	case <-c.interruption.done:
		return false
	default:
		return true
	}
}

func (c *Controller) releaseInterruptionLocked() {
	if !c.interrupted {
		return
	}
	c.interrupted = false
	c.interruption = nil
	for _, owned := range c.agents {
		owned.agent.ReleaseInterrupt()
	}
}

var ErrInterrupted = agent.ErrInterrupted
