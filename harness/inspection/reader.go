// Package inspection provides read-only, fixed-prefix views of session logs.
// Readers never construct agents, invoke tools, or resume archived execution.
package inspection

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
)

var ErrClosed = errors.New("inspection reader closed")

// Source is the read-only portion of a session store. Implementations must honor
// context cancellation and keep accepted records immutable until disposal.
type Source interface {
	Head(context.Context) (eventlog.Head, error)
	Read(context.Context, eventlog.Query) (eventlog.Page, error)
}

type Reader struct {
	source   Source
	session  string
	life     context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	active   sync.WaitGroup
	done     chan struct{}
	closeErr error
	owned    interface{ Close(context.Context) error }
}

// New borrows source; closing the reader never closes the source.
func New(ctx context.Context, source Source) (*Reader, error) {
	if source == nil {
		return nil, errors.New("inspection source is required")
	}
	head, err := source.Head(ctx)
	if err != nil {
		return nil, err
	}
	if head.Cursor.Session == "" {
		return nil, eventlog.ErrSession
	}
	life, cancel := context.WithCancel(context.Background())
	return &Reader{source: source, session: head.Cursor.Session, life: life, cancel: cancel, done: make(chan struct{})}, nil
}

// OpenJSONL opens a finite archive and owns its file/index handles. An interrupted
// archive remains inspectable with failed source health; it never resumes execution.
func OpenJSONL(ctx context.Context, path string) (*Reader, error) {
	source, err := eventlog.OpenJSONL(ctx, path)
	if err != nil {
		return nil, err
	}
	r, err := New(ctx, source)
	if err != nil {
		_ = source.Close(context.Background())
		return nil, err
	}
	r.owned = source
	return r, nil
}

func (r *Reader) begin(ctx context.Context) (context.Context, func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return nil, nil, err
	}
	r.active.Add(1)
	r.mu.Unlock()
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.life, cancel)
	return call, func() { stop(); cancel(); r.active.Done() }, nil
}

func (r *Reader) Head(ctx context.Context) (eventlog.Head, error) {
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return eventlog.Head{}, err
	}
	defer done()
	head, err := r.source.Head(ctx)
	if err == nil && head.Cursor.Session != r.session {
		return head, eventlog.ErrSession
	}
	return head, err
}

// Close prevents new queries, cancels and joins active reads, then closes owned
// archive storage. A deadline limits waiting, not cleanup; repeated Close joins it.
func (r *Reader) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.cancel()
		go func() {
			r.active.Wait()
			if r.owned != nil {
				r.closeErr = r.owned.Close(context.Background())
			}
			close(r.done)
		}()
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return r.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// View is immutable metadata through one exact log prefix. Content is read on
// demand. It remains usable only while its reader and source remain available.
type View struct {
	reader     *Reader
	log        Source
	id         string
	through    eventlog.Cursor
	projection *projection.Projector
}

func (v *View) Through() eventlog.Cursor { return v.through }

// At replays exactly through the requested cursor. Zero captures the current head;
// an explicit session cursor at sequence zero represents the empty prefix.
func (r *Reader) At(ctx context.Context, through eventlog.Cursor) (*View, error) {
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	head, err := r.source.Head(ctx)
	if err != nil {
		return nil, err
	}
	if head.Cursor.Session != r.session {
		return nil, eventlog.ErrSession
	}
	if through == (eventlog.Cursor{}) {
		through = head.Cursor
	}
	if through.Session != r.session {
		return nil, eventlog.ErrSession
	}
	if through.Sequence > head.Cursor.Sequence {
		return nil, eventlog.ErrFuture
	}
	p := projection.New(identity.SessionID(r.session))
	for p.Cursor().Sequence < through.Sequence {
		after := p.Cursor().Sequence
		page, err := r.source.Read(ctx, eventlog.Query{After: after, Limit: int(min(uint64(64), through.Sequence-after)), MaxBytes: 4 << 20})
		if err != nil {
			return nil, err
		}
		if len(page.Events) == 0 {
			return nil, errors.New("inspection cannot reach accepted prefix")
		}
		for _, e := range page.Events {
			if e.Sequence != p.Cursor().Sequence+1 || e.Sequence > through.Sequence {
				return nil, errors.New("invalid source page sequence")
			}
			if err = p.Apply(e); err != nil {
				return nil, fmt.Errorf("project record %d: %w", e.Sequence, err)
			}
		}
	}
	return &View{reader: r, log: r.source, id: r.session, through: through, projection: p}, nil
}

// ReadRecord reads the immutable stored record, without materializing a framed body.
func (v *View) ReadRecord(ctx context.Context, cursor eventlog.Cursor) (eventlog.Record, error) {
	ctx, done, err := v.reader.begin(ctx)
	if err != nil {
		return eventlog.Record{}, err
	}
	defer done()
	return v.readRecord(ctx, cursor)
}
func (v *View) readRecord(ctx context.Context, cursor eventlog.Cursor) (eventlog.Record, error) {
	if cursor.Session != v.id || cursor.Sequence == 0 {
		return eventlog.Record{}, eventlog.ErrSession
	}
	if cursor.Sequence > v.through.Sequence {
		return eventlog.Record{}, eventlog.ErrFuture
	}
	page, err := v.log.Read(ctx, eventlog.Query{After: cursor.Sequence - 1, Limit: 1})
	if err != nil {
		return eventlog.Record{}, err
	}
	if len(page.Events) != 1 || page.Events[0].Cursor() != cursor {
		return eventlog.Record{}, errors.New("missing inspection record")
	}
	return page.Events[0].Clone(), nil
}
