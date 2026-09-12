package harness

import (
	"context"
	"errors"
	"io"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/inbox"
)

func (s *Session) ID() string { return s.id }
func (s *Session) publish(e conversation.Event) {
	d, err := conversation.EncodeEvent(e)
	if err != nil {
		s.log.Fail(err)
		return
	}
	_ = s.log.Publish(d)
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
		v, err := conversation.DecodeEvent(e)
		if err != nil || v != nil {
			return v, err
		}
	}
}

// Dispose releases event storage after execution finalization. Snapshot state is
// preserved, but further history reads fail. Capture errors do not prevent cleanup.
func (s *Session) Dispose(ctx context.Context) error {
	closeErr := s.Close(ctx)
	if s.State() != Closed {
		return closeErr
	}
	if s.log == nil {
		return closeErr
	}
	return errors.Join(closeErr, s.log.Dispose(ctx))
}
