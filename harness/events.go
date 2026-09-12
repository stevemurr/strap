package harness

import (
	"context"
	"errors"
	"io"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/inbox"
)

func (s *Session) ID() string { return s.id }
func (s *Session) publish(e conversation.Event) error {
	if exit, ok := e.(conversation.AgentExited); ok && exit.Err != nil && !errors.Is(exit.Err, context.Canceled) {
		s.mu.Lock()
		s.executionError = errors.Join(s.executionError, exit.Err)
		s.mu.Unlock()
	}
	d, err := eventcodec.EncodeEvent(e)
	if err != nil {
		s.log.Fail(err)
		return err
	}
	if err := s.log.Publish(d); err != nil {
		s.log.Fail(err)
		return err
	}
	if batch, ok := e.(conversation.ToolBatchEvent); ok && s.telemetry != nil {
		s.telemetry.schedule(batch)
	}
	return nil
}
func (s *Session) Events(ctx context.Context, q eventlog.Query) (eventlog.Page, error) {
	return s.log.Read(ctx, q)
}
func (s *Session) Subscribe(after uint64) *eventlog.Subscription { return s.log.Subscribe(after) }
func (s *Session) Capture() eventlog.Status                      { return s.log.Status() }
func (s *Session) FlushEvents(ctx context.Context) error         { return s.log.Flush(ctx) }

// NextEvent is a compatibility reader with its own cursor. New adapters should
// use Subscribe so detaching/reconnecting never steals another reader's events.
func (s *Session) NextEvent(ctx context.Context) (conversation.Event, error) {
	s.legacyOnce.Do(func() { s.legacy = s.Subscribe(0) })
	for {
		e, err := s.legacy.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil, inbox.ErrClosed
		}
		if err != nil {
			return nil, err
		}
		v, err := eventcodec.DecodeEvent(e)
		if err != nil || v != nil {
			return v, err
		}
	}
}

// Dispose releases event storage after execution finalization. Snapshot state is
// preserved, but further history reads fail. Capture errors do not prevent cleanup.
func (s *Session) Dispose(ctx context.Context) error {
	closeErr := s.Close(ctx)
	if state := s.State(); state != Closed && state != Disposed {
		return closeErr
	}
	var disposeErr error
	if s.log != nil {
		disposeErr = s.log.Dispose(ctx)
	}
	if disposeErr == nil {
		s.mu.Lock()
		s.state = Disposed
		s.mu.Unlock()
	}
	return errors.Join(closeErr, disposeErr)
}

// Log appends structured host diagnostics to the canonical event sequence.
// It does not send model input. Payloads may contain sensitive task data; durable
// capture is explicitly selected through EventConfig.JSONLPath.
func (s *Session) Log(ctx context.Context, entry conversation.DiagnosticEvent) error {
	_, done, err := s.admission.Begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	switch entry.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("invalid diagnostic level")
	}
	d, err := eventcodec.EncodeEvent(entry)
	if err != nil {
		return err
	}
	return s.log.Publish(d)
}

// readWorkflow follows accepted controller facts. The workflow dispatcher never
// acknowledges its own writes and does not own a second runtime event queue.
func (s *Session) readWorkflow(ctx context.Context) (conversation.Event, error) {
	for {
		p, err := s.log.Read(ctx, eventlog.Query{After: s.workflowAfter, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(p.Events) > 0 {
			s.workflowAfter = p.Next
			e := p.Events[0]
			if e.Kind != "ack" && e.Kind != "agent_exited" {
				continue
			}
			return eventcodec.DecodeEvent(e)
		}
		if p.Head.State == eventlog.Failed {
			return nil, eventlog.ErrCapture
		}
		select {
		case <-s.controller.Done():
			return nil, inbox.ErrClosed
		default:
		}
		run, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(s.workflowReadLife, cancel)
		_, err = s.log.Wait(run, p.Head.Cursor)
		stop()
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return nil, err
		}
	}
}
