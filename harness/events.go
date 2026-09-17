package harness

import (
	"context"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/message"
	"io"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/inbox"
)

func (s *Session) ID() string { return s.id }
func (s *Session) publish(e conversation.Event) error {
	if v, ok := e.(conversation.MessageEvent); ok && v.Message.ID == "" {
		v.Message.ID = message.MessageID(fmt.Sprintf("host-%d", s.hostMessage.Add(1)))
		e = v
	}
	if exit, ok := e.(conversation.AgentExited); ok && exit.Err != nil && !errors.Is(exit.Err, context.Canceled) {
		s.mu.Lock()
		s.executionError = errors.Join(s.executionError, exit.Err)
		s.mu.Unlock()
		if exit.Agent == s.Root() {
			// Nothing can deliver a worker's result once the root is gone;
			// letting workers run only burns budget and fails their sends
			// with "agent stopped". Publish runs on the controller's event
			// path, so the stops go through a goroutine rather than reenter it.
			go s.stopWorkers(exit.Agent)
		}
	}
	if err := s.encoder.Publish(context.Background(), e); err != nil {
		s.log.Fail(err)
		return err
	}
	if batch, ok := e.(conversation.ToolBatchEvent); ok && s.telemetry != nil {
		s.telemetry.schedule(batch)
	}
	return nil
}

// stopWorkers requests a stop for every live agent other than the root.
func (s *Session) stopWorkers(root message.ActorID) {
	for _, a := range s.Agents() {
		if a.ID == root || a.State.Terminal() {
			continue
		}
		_, _ = s.StopAgent(a.ID)
	}
}

func (s *Session) Events(ctx context.Context, q eventlog.Query) (eventlog.Page, error) {
	return s.log.Read(ctx, q)
}

type SubscribeOptions struct {
	After eventlog.Cursor `json:"after"`
}

func (s *Session) Subscribe(ctx context.Context, options SubscribeOptions) (*eventlog.Subscription, error) {
	return s.log.Subscribe(ctx, options.After)
}
func (s *Session) Capture() eventlog.Status              { return s.log.Status() }
func (s *Session) FlushEvents(ctx context.Context) error { return s.log.Flush(ctx) }

// NextEvent is a compatibility reader with its own cursor. New adapters should
// use Subscribe so detaching/reconnecting never steals another reader's events.
func (s *Session) NextEvent(ctx context.Context) (conversation.Event, error) {
	s.legacyOnce.Do(func() { s.legacy, _ = s.Subscribe(context.Background(), SubscribeOptions{}) })
	if s.legacy == nil {
		return nil, eventlog.ErrDisposed
	}
	for {
		e, err := s.legacy.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil, inbox.ErrClosed
		}
		if err != nil {
			return nil, err
		}
		v, err := s.decodeRecord(ctx, e)
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
		return interruptionError(err)
	}
	defer done()
	switch entry.Level {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("invalid diagnostic level")
	}
	return s.publish(entry)
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
			return s.decodeRecord(ctx, e)
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
