package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"unicode/utf8"
)

var ErrReadQuery = errors.New("invalid recovery read query")

type OutputInspection struct {
	Output projection.OutputView `json:"output"`
	Source eventlog.Head         `json:"source"`
}
type OutputTextQuery struct {
	Output   identity.OutputID
	Through  eventlog.Cursor
	Offset   uint64
	MaxBytes int
}
type TextPage struct {
	Through eventlog.Cursor `json:"through"`
	Offset  uint64          `json:"offset"`
	Text    string          `json:"text"`
	Next    uint64          `json:"next"`
	End     bool            `json:"end"`
}
type ContentQuery struct {
	ID       identity.ContentID
	Offset   uint64
	MaxBytes int
}
type ContentPage struct {
	Ref    eventlog.ContentRef `json:"ref"`
	Offset uint64              `json:"offset"`
	Data   []byte              `json:"data"`
	Next   uint64              `json:"next"`
	End    bool                `json:"end"`
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
	if err := s.project(ctx); err != nil {
		return OutputInspection{}, err
	}
	o, err := s.projection.Output(id)
	if err != nil {
		return OutputInspection{}, err
	}
	head, err := s.log.Head(ctx)
	return OutputInspection{Output: o, Source: head}, err
}
func (s *Session) ReadOutputText(ctx context.Context, q OutputTextQuery) (TextPage, error) {
	page := TextPage{Through: q.Through, Offset: q.Offset, Next: q.Offset}
	if q.MaxBytes < 1 || q.MaxBytes > 1<<20 {
		return page, fmt.Errorf("%w: max_bytes must be between 1 and 1048576", ErrReadQuery)
	}
	head, err := s.log.Head(ctx)
	if err != nil {
		return page, err
	}
	if q.Through.Session != s.id {
		return page, eventlog.ErrSession
	}
	if q.Through.Sequence > head.Cursor.Sequence {
		return page, eventlog.ErrFuture
	}
	var after, total uint64
	found := false
	full := false
	for after < q.Through.Sequence {
		p, err := s.log.Read(ctx, eventlog.Query{After: after, Limit: 64, MaxBytes: 4 << 20})
		if err != nil {
			return page, err
		}
		if len(p.Events) == 0 {
			return page, errors.New("missing output prefix")
		}
		for _, e := range p.Events {
			if e.Sequence > q.Through.Sequence {
				break
			}
			after = e.Sequence
			if e.Output == nil || *e.Output != q.Output {
				continue
			}
			if e.Kind == "output_started" {
				found = true
			}
			if e.Kind != "output_delta" {
				continue
			}
			var d struct {
				Offset uint64 `json:"offset"`
				Text   string `json:"text"`
			}
			if err := json.Unmarshal(e.Payload, &d); err != nil {
				return page, err
			}
			if d.Offset != total || !utf8.ValidString(d.Text) {
				return page, errors.New("invalid output text sequence")
			}
			end := total + uint64(len(d.Text))
			if !full && q.Offset < end {
				start := uint64(0)
				if q.Offset > total {
					start = q.Offset - total
				}
				if start > 0 && !utf8.RuneStart(d.Text[start]) {
					return page, fmt.Errorf("%w: offset is not a UTF-8 boundary", ErrReadQuery)
				}
				rest := d.Text[start:]
				n := min(q.MaxBytes-len(page.Text), len(rest))
				for n > 0 && n < len(rest) && !utf8.RuneStart(rest[n]) {
					n--
				}
				if n == 0 && page.Text == "" {
					return page, eventlog.ErrPageSize
				}
				page.Text += rest[:n]
				page.Next += uint64(n)
				if n < len(rest) || len(page.Text) == q.MaxBytes {
					full = true
				}
			}
			total = end
		}
	}
	if !found {
		return page, projection.ErrNotFound
	}
	if q.Offset > total {
		return page, eventlog.ErrFuture
	}
	page.End = page.Next == total
	return page, nil
}
func (s *Session) ReadContent(ctx context.Context, q ContentQuery) (ContentPage, error) {
	if err := s.project(ctx); err != nil {
		return ContentPage{}, err
	}
	ref, err := s.projection.Content(q.ID)
	if err != nil {
		return ContentPage{}, err
	}
	return s.readContent(ctx, ref, q.Offset, q.MaxBytes)
}
func (s *Session) readContent(ctx context.Context, ref eventlog.ContentRef, offset uint64, budget int) (ContentPage, error) {
	out := ContentPage{Ref: ref, Offset: offset, Next: offset}
	if budget < 1 || budget > 1<<20 {
		return out, fmt.Errorf("%w: max_bytes must be between 1 and 1048576", ErrReadQuery)
	}
	if offset > ref.Bytes {
		return out, eventlog.ErrFuture
	}
	sum := sha256.New()
	var total uint64
	after := ref.First.Sequence - 1
	for after < ref.Last.Sequence {
		p, err := s.log.Read(ctx, eventlog.Query{After: after, Limit: 64, MaxBytes: 4 << 20})
		if err != nil {
			return out, err
		}
		if len(p.Events) == 0 {
			return out, errors.New("missing content records")
		}
		for _, e := range p.Events {
			if e.Sequence > ref.Last.Sequence {
				break
			}
			after = e.Sequence
			if e.Kind != "content_chunk" {
				continue
			}
			var c record.ContentChunk
			if err := json.Unmarshal(e.Payload, &c); err != nil {
				return out, err
			}
			if c.ID != ref.ID {
				continue
			}
			if c.Offset != total {
				return out, errors.New("content offset mismatch")
			}
			sum.Write(c.Data)
			end := total + uint64(len(c.Data))
			if offset < end && len(out.Data) < budget {
				start := uint64(0)
				if offset > total {
					start = offset - total
				}
				n := min(budget-len(out.Data), len(c.Data)-int(start))
				out.Data = append(out.Data, c.Data[start:start+uint64(n)]...)
				out.Next += uint64(n)
			}
			total = end
		}
	}
	if total != ref.Bytes || hex.EncodeToString(sum.Sum(nil)) != ref.SHA256 {
		return ContentPage{}, errors.New("content integrity failure")
	}
	out.End = out.Next == ref.Bytes
	return out, nil
}

// ResolveRecord materializes an explicitly requested payload for a view or host
// adapter. Its identity is checked against the immutable record in this session.
func (s *Session) ResolveRecord(ctx context.Context, e eventlog.Record) (eventlog.Record, error) {
	if e.Session != s.id || e.Sequence == 0 {
		return eventlog.Record{}, eventlog.ErrSession
	}
	p, err := s.log.Read(ctx, eventlog.Query{After: e.Sequence - 1, Limit: 1})
	if err != nil {
		return e, err
	}
	if len(p.Events) == 0 {
		return e, eventlog.ErrFuture
	}
	e = p.Events[0]
	f, ok := record.Frame(e.Payload)
	if !ok {
		return e, nil
	}
	if err = s.project(ctx); err != nil {
		return e, err
	}
	ref, err := s.projection.Content(f.Content.ID)
	if err != nil {
		return e, err
	}
	var body []byte
	for offset := uint64(0); offset < ref.Bytes; {
		page, err := s.readContent(ctx, ref, offset, 1<<20)
		if err != nil {
			return e, err
		}
		body = append(body, page.Data...)
		offset = page.Next
	}
	e.Payload = body
	return e, nil
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

// InspectAgentContext returns only state derived from an accepted log prefix.
func (s *Session) InspectAgentContext(ctx context.Context, id identity.ActorID, opts conversation.InspectOptions) (conversation.AgentInspection, error) {
	if err := s.project(ctx); err != nil {
		return conversation.AgentInspection{}, err
	}
	info, err := s.projection.AgentInspection(id)
	if errors.Is(err, projection.ErrNotFound) {
		return info, conversation.ErrAgentNotFound
	}
	if err != nil {
		return info, err
	}
	if opts.Transcript == nil {
		return info, nil
	}
	q := *opts.Transcript
	if q.Limit == 0 {
		q.Limit = agent.DefaultTranscriptLimit
	}
	if q.Limit < 1 || q.Limit > agent.MaxTranscriptLimit {
		return info, agent.ErrInvalidQuery
	}
	if q.Before == 0 {
		q.Before = info.ContextRevision + 1
	}
	entries, err := s.projection.History(id, q.Before, q.Limit)
	if err != nil {
		return info, fmt.Errorf("%w: %v", agent.ErrInvalidQuery, err)
	}
	page := agent.TranscriptPage{Entries: make([]agent.TranscriptEntry, 0, len(entries))}
	if len(entries) > 0 {
		page.HasEarlier = entries[0].Position > 1
	}
	for _, h := range entries {
		e, err := s.ResolveRecord(ctx, eventlog.Record{Session: s.id, Sequence: h.Record.Sequence})
		if err != nil {
			return info, err
		}
		var fact agent.HistoryAppended
		if err = json.Unmarshal(e.Payload, &fact); err != nil {
			return info, err
		}
		page.Entries = append(page.Entries, agent.TranscriptEntry{Position: h.Position, Message: fact.Message})
	}
	info.Transcript = &page
	return info, nil
}
