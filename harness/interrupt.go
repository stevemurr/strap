package harness

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/internal/admission"
)

type interruptAttempt struct {
	done chan struct{}
	err  error
}

// ErrInterrupted means an interruption is settling or execution is held until
// new user input. Reads remain available; interruption is not session closure.
var ErrInterrupted = conversation.ErrInterrupted

// Interrupt is the user-facing Stop operation. It cancels every current agent
// exchange, settles tool history, cancels nonterminal delegated work, and drops
// queued deliveries with undelivered receipts. It preserves agent identities,
// history, completed actions and session resources. Send starts a fresh exchange.
//
// The context limits only the wait. A timeout does not release the execution
// fence; repeated calls join the same attempt until new user input is accepted.
// Tools that ignore cancellation can delay settlement. This never rolls back or
// automatically retries their external effects.
func (s *Session) Interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.state != Open {
		s.mu.Unlock()
		return ErrClosed
	}
	a := s.interruption
	if a == nil {
		a = &interruptAttempt{done: make(chan struct{})}
		s.interruption = a
		s.workflow.Suspend()
		drained := s.admission.Suspend()
		go s.finishInterruption(a, drained)
	}
	s.mu.Unlock()
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) finishInterruption(a *interruptAttempt, drained <-chan struct{}) {
	err := s.controller.Interrupt(context.Background())
	<-drained
	err = errors.Join(err, s.workflow.SettleInterrupt(context.Background()))
	if err == nil {
		err = s.publish(conversation.DiagnosticEvent{Level: "info", Message: "Execution interrupted; waiting for new user input", Fields: map[string]string{"operation": "interrupt", "status": "completed"}})
	}
	s.mu.Lock()
	a.err = err
	if err == nil {
		s.admission.Resume()
	}
	close(a.done)
	s.mu.Unlock()
}

func interruptionError(err error) error {
	if errors.Is(err, admission.ErrSuspended) {
		return ErrInterrupted
	}
	return err
}
