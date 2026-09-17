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
		s.cancelExecution()
	}
	a := &closeAttempt{done: make(chan struct{})}
	s.attempt = a
	go s.finalize(a)
	return a
}
func (s *Session) finalize(a *closeAttempt) {
	err := s.admission.Wait(context.Background())
	s.mu.Lock()
	interruption := s.interruption
	s.mu.Unlock()
	if interruption != nil {
		<-interruption.done
	}
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
