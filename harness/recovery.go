package harness

import (
	"context"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

var ErrReadQuery = inspection.ErrReadQuery

type OutputInspection struct {
	Output projection.OutputView `json:"output"`
	Source eventlog.Head         `json:"source"`
}
type OutputTextQuery struct {
	Channel  provider.OutputChannel
	Output   identity.OutputID
	Through  eventlog.Cursor
	Offset   uint64
	MaxBytes int
}
type TextPage = inspection.TextPage
type ContentQuery = inspection.ContentQuery
type ContentPage = inspection.ContentPage

// Trace creates an independent borrowed reader. Closing it does not close the
// session; disposing the session's store makes borrowed readers unavailable.
func (s *Session) Trace(ctx context.Context) (*inspection.Reader, error) {
	return inspection.New(ctx, s.log)
}
func (s *Session) traceView(ctx context.Context, cursor eventlog.Cursor) (*inspection.Reader, *inspection.View, error) {
	r, err := s.Trace(ctx)
	if err != nil {
		return nil, nil, err
	}
	v, err := r.At(ctx, cursor)
	if err != nil {
		_ = r.Close(context.Background())
		return nil, nil, err
	}
	return r, v, nil
}
func (s *Session) project(ctx context.Context) error {
	head, err := s.log.Head(ctx)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.projectionGate <- struct{}{}:
		defer func() { <-s.projectionGate }()
	}
	if s.projectionError != nil {
		return s.projectionError
	}
	for s.projection.Cursor().Sequence < head.Cursor.Sequence {
		p, err := s.log.Read(ctx, eventlog.Query{After: s.projection.Cursor().Sequence, Limit: 64, MaxBytes: 4 << 20})
		if err != nil {
			return err
		}
		if len(p.Events) == 0 {
			return errors.New("projection cannot reach accepted head")
		}
		for _, e := range p.Events {
			if err := s.projection.Apply(e); err != nil {
				s.projectionError = fmt.Errorf("project record %d: %w", e.Sequence, err)
				return s.projectionError
			}
		}
	}
	return nil
}

func (s *Session) InspectOutput(ctx context.Context, id identity.OutputID) (OutputInspection, error) {
	r, v, err := s.traceView(ctx, eventlog.Cursor{})
	if err != nil {
		return OutputInspection{}, err
	}
	defer r.Close(context.Background())
	o, err := v.InspectOutput(ctx, id)
	if err != nil {
		return OutputInspection{}, err
	}
	head, err := r.Head(ctx)
	return OutputInspection{Output: o.OutputView, Source: head}, err
}
func (s *Session) ReadOutputText(ctx context.Context, q OutputTextQuery) (TextPage, error) {
	if q.Through.Session != s.id {
		return TextPage{}, eventlog.ErrSession
	}
	r, v, err := s.traceView(ctx, q.Through)
	if err != nil {
		return TextPage{}, err
	}
	defer r.Close(context.Background())
	return v.ReadOutputText(ctx, inspection.OutputTextQuery{Channel: q.Channel, Output: q.Output, Offset: q.Offset, MaxBytes: q.MaxBytes})
}
func (s *Session) ReadContent(ctx context.Context, q ContentQuery) (ContentPage, error) {
	r, v, err := s.traceView(ctx, eventlog.Cursor{})
	if err != nil {
		return ContentPage{}, err
	}
	defer r.Close(context.Background())
	return v.ReadContent(ctx, q)
}
func (s *Session) ResolveRecord(ctx context.Context, e eventlog.Record) (eventlog.Record, error) {
	r, err := s.Trace(ctx)
	if err != nil {
		return e, err
	}
	defer r.Close(context.Background())
	return r.ResolveRecord(ctx, e)
}
func (s *Session) decodeRecord(ctx context.Context, e eventlog.Record) (conversation.Event, error) {
	if e.Kind == "content_chunk" {
		return nil, nil
	}
	resolved, err := s.ResolveRecord(ctx, e)
	if err != nil {
		return nil, err
	}
	return eventcodec.DecodeEvent(resolved)
}
func (s *Session) InspectAgentContext(ctx context.Context, id identity.ActorID, opts conversation.InspectOptions) (AgentInspection, error) {
	r, v, err := s.traceView(ctx, eventlog.Cursor{})
	if err != nil {
		return AgentInspection{}, err
	}
	defer r.Close(context.Background())
	return v.InspectAgentContext(ctx, id, opts)
}
