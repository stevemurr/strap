package agent

import (
	"context"
	"errors"
	"sync"
)

// State is the acknowledged position of an agent's loop, including pending controls.
type State string

const (
	Idle           State = "idle"
	Running        State = "running"
	PauseRequested State = "pause_requested"
	Paused         State = "paused"
	StopRequested  State = "stop_requested"
	Stopped        State = "stopped"
	Failed         State = "failed"
)

func (s State) Terminal() bool { return s == Stopped || s == Failed }

type lifecycle struct {
	mu         sync.Mutex
	state      State
	changed    chan struct{}
	waitCancel context.CancelFunc
}

func (a *Agent) State() State {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	return a.control.state
}

// setStateLocked publishes ordered transitions. OnState must only enqueue the
// notification; it must not call back into this agent or block on a consumer.
func (a *Agent) setStateLocked(state State) {
	if a.control.state == state {
		return
	}
	a.control.state = state
	close(a.control.changed)
	a.control.changed = make(chan struct{})
	if a.config.OnState != nil {
		a.config.OnState(state)
	}
}

// Pause requests a boundary pause without canceling a model/tool operation.
func (a *Agent) Pause() (State, error) {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	state := a.control.state
	if state.Terminal() || state == StopRequested {
		return state, errors.New("agent is stopping or stopped")
	}
	if state != Paused && state != PauseRequested {
		a.setStateLocked(PauseRequested)
	}
	if a.control.waitCancel != nil {
		a.control.waitCancel()
	}
	return a.control.state, nil
}

// Resume releases a pause or retracts a pending pause. The loop reports its next
// active state when it reaches the boundary; it never restarts a stopped agent.
func (a *Agent) Resume() (State, error) {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	state := a.control.state
	if state.Terminal() || state == StopRequested {
		return state, errors.New("agent is stopping or stopped")
	}
	if state == Paused || state == PauseRequested {
		a.setStateLocked(Running)
	}
	return a.control.state, nil
}

// RequestStop closes admission to further loop operations. The owner also
// cancels Run's context so an in-flight provider or tool can terminate.
func (a *Agent) RequestStop() State {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	if !a.control.state.Terminal() {
		a.setStateLocked(StopRequested)
	}
	if a.control.waitCancel != nil {
		a.control.waitCancel()
	}
	return a.control.state
}

func (a *Agent) checkpoint(ctx context.Context) error {
	for {
		a.control.mu.Lock()
		if err := ctx.Err(); err != nil {
			a.control.mu.Unlock()
			return err
		}
		switch a.control.state {
		case StopRequested, Stopped, Failed:
			a.control.mu.Unlock()
			return context.Canceled
		case PauseRequested, Paused:
			a.setStateLocked(Paused)
			changed := a.control.changed
			a.control.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
			}
		default:
			a.setStateLocked(Running)
			a.control.mu.Unlock()
			return nil
		}
	}
}

// waitInbox waits without removing input. Pause interrupts only this idle wait,
// leaving queued messages unconsumed until a subsequent checkpoint admits work.
func (a *Agent) waitInbox(ctx context.Context) error {
	for {
		if err := a.checkpoint(ctx); err != nil {
			return err
		}
		a.control.mu.Lock()
		if a.control.state != Running {
			a.control.mu.Unlock()
			continue
		}
		a.setStateLocked(Idle)
		waitCtx, cancel := context.WithCancel(ctx)
		a.control.waitCancel = cancel
		a.control.mu.Unlock()
		err := a.config.Inbox.Wait(waitCtx)
		a.control.mu.Lock()
		a.control.waitCancel = nil
		a.control.mu.Unlock()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			continue
		}
		if err != nil {
			return err
		}
		return a.checkpoint(ctx)
	}
}
