package harness

import (
	"context"
	"github.com/stevemurr/strap/eventlog"
	"time"

	"github.com/stevemurr/strap/internal/admission"
)

type State string

const (
	Open    State = "open"
	Closing State = "closing"
	Closed  State = "closed"
)

var ErrClosed = admission.ErrClosed

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
func (s *Session) startClose() *closeAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempt != nil {
		return s.attempt
	}
	if s.state == Open {
		s.state = Closing
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
	if err == nil && s.workflow != nil {
		err = s.workflow.Close(context.Background())
	} else if err == nil && s.controller != nil {
		err = s.controller.Close(context.Background())
	}
	if s.telemetry != nil {
		s.telemetry.wg.Wait()
	}
	if err == nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = s.resources.Close(cleanup)
		cancel()
	}
	cleanupOK := err == nil
	if cleanupOK && s.log != nil {
		err = s.log.Finish(context.Background(), eventlog.Outcome{})
	}
	s.mu.Lock()
	a.err = err
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
