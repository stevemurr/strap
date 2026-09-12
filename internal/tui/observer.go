package tui

import (
	"context"
	"errors"
	"io"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
)

type observedSession struct {
	Session
	sub *eventlog.Subscription
}

func observeSession(s Session) (Session, func()) {
	if source, ok := s.(interface {
		Subscribe(uint64) *eventlog.Subscription
	}); ok {
		sub := source.Subscribe(0)
		return &observedSession{Session: s, sub: sub}, sub.Close
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
		v, err := conversation.DecodeEvent(e)
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
