package agent

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
)

type interruption struct {
	done    chan struct{}
	settled bool
}

// RequestInterrupt cancels the current exchange, including an inbox wait. The
// returned channel closes after execution and history settlement. ReleaseInterrupt
// is a host-only operation after all agents and queued deliveries are settled.
func (a *Agent) RequestInterrupt() <-chan struct{} {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	if a.control.interrupt == nil {
		a.control.interrupt = &interruption{done: make(chan struct{})}
	}
	if a.control.exchangeCancel != nil {
		a.control.exchangeCancel()
	}
	if a.control.state.Terminal() && !a.control.interrupt.settled {
		a.control.interrupt.settled = true
		close(a.control.interrupt.done)
	}
	return a.control.interrupt.done
}

func (a *Agent) ReleaseInterrupt() {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	if a.control.interrupt != nil && a.control.interrupt.settled {
		a.control.interrupt = nil
		close(a.control.changed)
		a.control.changed = make(chan struct{})
	}
}

func (a *Agent) interruptPending() bool {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	return a.control.interrupt != nil
}

func (a *Agent) beginExchange(ctx context.Context) (context.Context, context.CancelFunc, error) {
	for {
		a.control.mu.Lock()
		if err := ctx.Err(); err != nil {
			a.control.mu.Unlock()
			return nil, nil, err
		}
		// The host is closing. Any interruption has already settled by now, so
		// only the wait for a release remains and nothing is left to fence.
		if a.quiescing.Load() {
			a.control.mu.Unlock()
			return nil, nil, errQuiesced
		}
		if a.control.interrupt != nil {
			settled, changed := a.control.interrupt.settled, a.control.changed
			a.control.mu.Unlock()
			if !settled {
				if err := a.settleInterrupt(); err != nil {
					return nil, nil, err
				}
				continue
			}
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-changed:
			}
			continue
		}
		run, cancel := context.WithCancel(ctx)
		a.control.exchangeCancel = cancel
		a.control.mu.Unlock()
		return run, cancel, nil
	}
}

// finishInterrupt also releases waiters if termination or capture failure wins.
func (a *Agent) finishInterrupt() {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	a.control.exchangeCancel = nil
	if a.control.interrupt != nil && !a.control.interrupt.settled {
		a.control.interrupt.settled = true
		close(a.control.interrupt.done)
	}
}

func (a *Agent) settleInterrupt() error {
	// A committed assistant tool batch must have one result per call before
	// another model request. Executed calls were appended by exchange; remaining
	// calls never ran and must not be replayed on the next instruction.
	for _, call := range a.thread.pendingCalls() {
		if _, err := a.appendHistory(provider.Message{Role: "tool", ToolCallID: call.ID,
			Content: content.Text("Cancelled before execution by user interruption. This call did not run.")}, nil); err != nil {
			return err
		}
	}
	if _, err := a.appendHistory(provider.Message{Role: "user", Content: content.Text("The user interrupted execution. Completed actions remain in effect; interrupted actions may have partial effects. Do not retry or continue the previous task automatically. Wait for a new instruction.")}, nil); err != nil {
		return err
	}
	a.repeated = repeatedCall{}
	a.emission.Lock()
	a.control.mu.Lock()
	if a.stopRequested.Load() {
		a.control.mu.Unlock()
		a.emission.Unlock()
		return context.Canceled
	}
	a.setStateLocked(Interrupted)
	a.unlockAndReportState()
	a.emission.Unlock()
	if err := a.reportError(); err != nil {
		return err
	}
	a.finishInterrupt()
	return nil
}

var ErrInterrupted = errors.New("execution interrupted; send a new instruction to continue")
