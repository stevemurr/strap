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
	Interrupted    State = "interrupted"
	StopRequested  State = "stop_requested"
	Stopped        State = "stopped"
	Failed         State = "failed"
)

func (s State) Terminal() bool { return s == Stopped || s == Failed }

type lifecycle struct {
	mu             sync.Mutex
	pending        *StateSnapshot
	state          State
	revision       uint64
	changed        chan struct{}
	waitCancel     context.CancelFunc
	exchangeCancel context.CancelFunc
	interrupt      *interruption
}

type StateSnapshot struct {
	State    State  `json:"state"`
	Revision uint64 `json:"state_revision"`
}

func (a *Agent) StateSnapshot() StateSnapshot {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	return StateSnapshot{a.control.state, a.control.revision}
}

func (a *Agent) State() State {
	a.control.mu.Lock()
	defer a.control.mu.Unlock()
	return a.control.state
}

// setStateLocked stages a transition under the control lock. The emission owner
// releases that lock before publishing the immutable state/revision pair.
func (a *Agent) setStateLocked(state State) {
	if a.control.state == state {
		return
	}
	a.control.state = state
	a.control.revision++
	close(a.control.changed)
	a.control.changed = make(chan struct{})
	a.control.pending = &StateSnapshot{state, a.control.revision}
}

// unlockAndReportState releases the control lock before required publication.
// emission serializes transitions, while stopRequested and cancellation bypass it.
func (a *Agent) unlockAndReportState() {
	p := a.control.pending
	a.control.pending = nil
	a.control.mu.Unlock()
	if p == nil {
		return
	}
	_ = a.report(*p)
	if a.config.OnLifecycle != nil {
		a.config.OnLifecycle(*p)
	}
	if a.config.OnState != nil {
		a.config.OnState(p.State)
	}
}
func (a *Agent) PauseSnapshot() (StateSnapshot, error) {
	a.emission.Lock()
	defer a.emission.Unlock()
	a.control.mu.Lock()
	if a.control.interrupt != nil {
		s := StateSnapshot{a.control.state, a.control.revision}
		a.control.mu.Unlock()
		return s, ErrInterrupted
	}
	if a.control.state.Terminal() || a.stopRequested.Load() {
		s := StateSnapshot{a.control.state, a.control.revision}
		a.control.mu.Unlock()
		return s, errors.New("agent is stopping or stopped")
	}
	if a.control.state != Paused && a.control.state != PauseRequested {
		a.setStateLocked(PauseRequested)
	}
	if a.control.waitCancel != nil {
		a.control.waitCancel()
	}
	s := StateSnapshot{a.control.state, a.control.revision}
	a.unlockAndReportState()
	return s, a.reportError()
}
func (a *Agent) Pause() (State, error) { s, e := a.PauseSnapshot(); return s.State, e }
func (a *Agent) ResumeSnapshot() (StateSnapshot, error) {
	a.emission.Lock()
	defer a.emission.Unlock()
	a.control.mu.Lock()
	if a.control.interrupt != nil {
		s := StateSnapshot{a.control.state, a.control.revision}
		a.control.mu.Unlock()
		return s, ErrInterrupted
	}
	if a.control.state.Terminal() || a.stopRequested.Load() {
		s := StateSnapshot{a.control.state, a.control.revision}
		a.control.mu.Unlock()
		return s, errors.New("agent is stopping or stopped")
	}
	if a.control.state == Paused || a.control.state == PauseRequested {
		a.setStateLocked(Running)
	}
	s := StateSnapshot{a.control.state, a.control.revision}
	a.unlockAndReportState()
	return s, a.reportError()
}
func (a *Agent) Resume() (State, error) { s, e := a.ResumeSnapshot(); return s.State, e }
func (a *Agent) RequestStopSnapshot() (StateSnapshot, error) {
	// This short control boundary also fences successful response commitment.
	a.control.mu.Lock()
	a.stopRequested.Store(true)
	if a.control.waitCancel != nil {
		a.control.waitCancel()
	}
	a.control.mu.Unlock()
	a.reporting.mu.Lock()
	if a.reporting.cancel != nil {
		a.reporting.cancel()
	}
	a.reporting.mu.Unlock()
	a.emission.Lock()
	defer a.emission.Unlock()
	a.control.mu.Lock()
	if !a.control.state.Terminal() {
		a.setStateLocked(StopRequested)
	}
	s := StateSnapshot{a.control.state, a.control.revision}
	a.unlockAndReportState()
	return s, a.reportError()
}
func (a *Agent) RequestStop() State                   { s, _ := a.RequestStopSnapshot(); return s.State }
func (a *Agent) checkpoint(ctx context.Context) error { return a.checkpointState(ctx, Running) }

// Waiting for an inbox must not advertise execution before a message arrives.
func (a *Agent) checkpointState(ctx context.Context, next State) error {
	for {
		if err := a.reportError(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		a.emission.Lock()
		a.control.mu.Lock()
		if a.stopRequested.Load() || ctx.Err() != nil {
			a.control.mu.Unlock()
			a.emission.Unlock()
			return context.Canceled
		}
		switch a.control.state {
		case StopRequested, Stopped, Failed:
			a.control.mu.Unlock()
			a.emission.Unlock()
			return context.Canceled
		case PauseRequested, Paused:
			a.setStateLocked(Paused)
			changed := a.control.changed
			a.unlockAndReportState()
			a.emission.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
			}
		default:
			a.setStateLocked(next)
			a.unlockAndReportState()
			a.emission.Unlock()
			return a.reportError()
		}
	}
}
func (a *Agent) waitInbox(ctx context.Context) error {
	for {
		if err := a.checkpointState(ctx, Idle); err != nil {
			return err
		}
		a.emission.Lock()
		a.control.mu.Lock()
		if a.control.state != Idle {
			a.control.mu.Unlock()
			a.emission.Unlock()
			continue
		}
		a.setStateLocked(Idle)
		waitCtx, cancel := context.WithCancel(ctx)
		a.control.waitCancel = cancel
		a.unlockAndReportState()
		a.emission.Unlock()
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
