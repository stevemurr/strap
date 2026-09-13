package inspection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"unicode/utf8"
)

var ErrReadQuery = errors.New("invalid recovery read query")

type OutputTextQuery struct {
	Channel  provider.OutputChannel
	Output   identity.OutputID
	Offset   uint64
	MaxBytes int
}
type TextPage struct {
	Channel provider.OutputChannel `json:"channel"`
	Through eventlog.Cursor        `json:"through"`
	Offset  uint64                 `json:"offset"`
	Text    string                 `json:"text"`
	Next    uint64                 `json:"next"`
	End     bool                   `json:"end"`
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

func (s *View) ReadOutputText(ctx context.Context, q OutputTextQuery) (TextPage, error) {
	ctx, done, beginErr := s.reader.begin(ctx)
	if beginErr != nil {
		return TextPage{}, beginErr
	}
	defer done()

	q.Channel = provider.NormalizeChannel(q.Channel)
	page := TextPage{Channel: q.Channel, Through: s.through, Offset: q.Offset, Next: q.Offset}
	if !q.Channel.Valid() {
		return page, fmt.Errorf("%w: channel", ErrReadQuery)
	}
	if q.MaxBytes < 1 || q.MaxBytes > 1<<20 {
		return page, fmt.Errorf("%w: max_bytes must be between 1 and 1048576", ErrReadQuery)
	}
	var after, total uint64
	found := false
	full := false
	for after < s.through.Sequence {
		p, err := s.log.Read(ctx, eventlog.Query{After: after, Limit: 64, MaxBytes: 4 << 20})
		if err != nil {
			return page, err
		}
		if len(p.Events) == 0 {
			return page, errors.New("missing output prefix")
		}
		for _, e := range p.Events {
			if e.Sequence > s.through.Sequence {
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
				Channel provider.OutputChannel `json:"channel"`
				Offset  uint64                 `json:"offset"`
				Text    string                 `json:"text"`
			}
			if err := json.Unmarshal(e.Payload, &d); err != nil {
				return page, err
			}
			channel, err := eventcodec.OutputChannel(e.Schema, d.Channel)
			if err != nil {
				return page, err
			}
			if channel != q.Channel {
				continue
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
func (s *View) ReadContent(ctx context.Context, q ContentQuery) (ContentPage, error) {
	ctx, done, beginErr := s.reader.begin(ctx)
	if beginErr != nil {
		return ContentPage{}, beginErr
	}
	defer done()

	ref, err := s.projection.Content(q.ID)
	if err != nil {
		return ContentPage{}, err
	}
	return s.readContent(ctx, ref, q.Offset, q.MaxBytes)
}
func (s *View) readContent(ctx context.Context, ref eventlog.ContentRef, offset uint64, budget int) (ContentPage, error) {
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
func (s *View) ResolveRecord(ctx context.Context, e eventlog.Record) (eventlog.Record, error) {
	ctx, done, beginErr := s.reader.begin(ctx)
	if beginErr != nil {
		return eventlog.Record{}, beginErr
	}
	defer done()

	stored, err := s.readRecord(ctx, e.Cursor())
	if err != nil {
		return eventlog.Record{}, err
	}
	e = stored
	f, ok := record.Frame(e.Payload)
	if !ok {
		return e, nil
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
