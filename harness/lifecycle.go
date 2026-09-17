package harness

import (
	"context"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"

	"github.com/stevemurr/strap/internal/admission"
)

type State string

const (
	Open     State = "open"
	Closing  State = "closing"
	Closed   State = "closed"
	Disposed State = "disposed"
)

var ErrClosed = admission.ErrClosed
var ErrBusy = admission.ErrBusy

type closeAttempt struct {
	done chan struct{}
	err  error
}

func (s *Session) State() State { s.mu.Lock(); defer s.mu.Unlock(); return s.state }

// Close initiates owner-controlled shutdown. Its context only limits this wait.
// Cleanup failures preserve ownership; a later call retries unfinished resources.
func (s *Session) Close(ctx context.Context) error {
	a := s.startClose()
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Session) startClose() *closeAttempt { return s.startCloseReason("requested") }
func (s *Session) startCloseReason(reason string) *closeAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt != nil {
		return s.attempt
	}
	if s.state == Open {
		s.state = Closing
		if s.startupError != "" {
			reason = "startup_failed"
		}
		s.outcome = &eventlog.Outcome{Reason: reason}
		s.admission.Seal()
		if s.workflow != nil {
			s.workflow.BeginClosing()
		}
		if s.telemetry != nil {
			s.telemetry.stop()
		}
	}
	a := &closeAttempt{done: make(chan struct{})}
	s.attempt = a
	go s.finalize(a)
	return a
}

// settleBudget bounds the graceful phase of a close. It is sized for reaching a
// checkpoint, not for finishing a model call: an idle or looping agent settles
// in microseconds, while one awaiting a response settles only when the provider
// answers, which no shutdown budget can usefully cover. Waiting longer would
// stall every close by that much and cancel the call anyway.
const settleBudget = 500 * time.Millisecond

func (s *Session) finalize(a *closeAttempt) {
	// An unsettled interruption fences the loops, so nothing can be asked to
	// stop until it finishes unwinding.
	s.mu.Lock()
	interruption := s.interruption
	s.mu.Unlock()
	if interruption != nil {
		<-interruption.done
	}
	// Ask the agents to stop before cancelling anything. Cancelling first, as
	// this used to, aborted whatever was in flight on every clean shutdown and
	// reported it as a failed output, a failed tool call and a failed agent.
	settle, stopSettle := context.WithTimeout(context.Background(), settleBudget)
	if s.workflow != nil {
		_ = s.workflow.Close(settle)
	} else if s.controller != nil {
		_ = s.controller.Close(settle)
	}
	stopSettle()
	s.cancelExecution()
	err := s.admission.Wait(context.Background())
	if err == nil && s.workflow != nil {
		err = s.workflow.Close(context.Background())
	} else if err == nil && s.controller != nil {
		err = s.controller.Close(context.Background())
	}
	if s.telemetry != nil {
		s.telemetry.wg.Wait()
	}
	if err == nil {
		s.mu.Lock()
		s.outcome.CleanupAttempts++
		s.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = s.resources.Close(cleanup)
		cancel()
	}
	cleanupOK := err == nil
	s.mu.Lock()
	s.outcome.CleanupError = ""
	if err != nil {
		s.outcome.CleanupError = eventlog.Summary(err.Error())
	}
	if s.executionError != nil {
		s.outcome.Error = eventlog.Summary(s.executionError.Error())
	}
	if s.startupError != "" {
		s.outcome.Error = eventlog.Summary(s.startupError)
	}
	outcome := *s.outcome
	s.mu.Unlock()
	if !cleanupOK && s.log != nil {
		s.publish(conversation.DiagnosticEvent{Level: "error", Message: "Session cleanup failed", Fields: map[string]string{"error": err.Error()}})
	}
	if cleanupOK && s.log != nil {
		err = s.log.Finish(context.Background(), outcome)
	}
	if s.log != nil {
		capture := s.log.Status()
		outcome.CaptureError = capture.CaptureError
		outcome.Omitted = capture.Omitted
	}

	s.mu.Lock()
	a.err = err
	s.outcome = &outcome
	if cleanupOK {
		s.state = Closed
		if s.stopOwner != nil {
			s.stopOwner()
		}
	} else {
		s.attempt = nil
	}
	close(a.done)
	s.mu.Unlock()
}
