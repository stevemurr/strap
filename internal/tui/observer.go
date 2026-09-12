package tui

import (
	"context"
	"errors"
	"io"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
)

type observedSession struct {
	Session
	sub        *eventlog.Subscription
	projection *projection.Projector
}

func observeSession(s Session) (Session, func()) {
	if source, ok := s.(interface {
		Subscribe(context.Context, harness.SubscribeOptions) (*eventlog.Subscription, error)
		ID() string
	}); ok {
		sub, err := source.Subscribe(context.Background(), harness.SubscribeOptions{})
		if err != nil {
			return &failedSession{Session: s, err: err}, func() {}
		}
		return &observedSession{Session: s, sub: sub, projection: projection.New(identity.SessionID(source.ID()))}, sub.Close
	}
	return s, func() {}
}
func (s *observedSession) NextEvent(ctx context.Context) (conversation.Event, error) {
	for {
		e, err := s.sub.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil, inbox.ErrClosed
		}
		if err != nil {
			return nil, err
		}
		if err := s.projection.Apply(e); err != nil {
			return nil, err
		}
		if e.Kind == "content_chunk" {
			continue
		}
		if resolver, ok := s.Session.(interface {
			ResolveRecord(context.Context, eventlog.Record) (eventlog.Record, error)
		}); ok {
			e, err = resolver.ResolveRecord(ctx, e)
			if err != nil {
				return nil, err
			}
		}
		v, err := eventcodec.DecodeEvent(e)
		if err != nil || v != nil {
			return v, err
		}
	}
}

// Keep the existing on-demand UI capability while observation is detached.
func (s *observedSession) CountAgentTokens(ctx context.Context, id message.ActorID, revision uint64) (int64, error) {
	if counter, ok := s.Session.(tokenSession); ok {
		return counter.CountAgentTokens(ctx, id, revision)
	}
	return 0, errors.New("token counting unavailable")
}

func (s *observedSession) AutomaticContextTokens() bool {
	if source, ok := s.Session.(interface{ AutomaticContextTokens() bool }); ok {
		return source.AutomaticContextTokens()
	}
	return false
}

type failedSession struct {
	Session
	err error
}

func (s *failedSession) NextEvent(context.Context) (conversation.Event, error) { return nil, s.err }
