package eventlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/stevemurr/strap/internal/admission"
)

type publication struct {
	data   Data
	done   chan struct{}
	cursor Cursor
	err    error
}

// Log acknowledges readable storage and owns one bounded writer. Failure signaling
// is independent of that writer; callers never wait for a client to consume data.
type Log struct {
	failureSink         func(error)
	store               Store
	limits              Limits
	mu                  sync.Mutex
	queue               []*publication
	jobs                map[*publication]struct{}
	pending, bytes      int
	producers           chan struct{}
	accepted, completed uint64
	err                 error
	finishing           bool
	outcome             Outcome
	wake                chan struct{}
	done                chan struct{}
	writeCtx            context.Context
	cancelWrite         context.CancelFunc
	writeTimeout        time.Duration
	reads               *admission.Gate
	cancelReads         context.CancelFunc
	disposeGate         chan struct{}
	disposed            bool
}

type Status struct {
	CaptureError string `json:"capture_error,omitempty"`
	Omitted      uint64 `json:"omitted"` // Always zero for a recoverable log.
	Sealed       bool   `json:"sealed"`
	Disposed     bool   `json:"disposed"`
}
type Option func(*Log)

// WithFailureSink runs once independently of blocked backend I/O. It must return
// promptly and must not synchronously wait for session/log finalization.
func WithFailureSink(f func(error)) Option { return func(l *Log) { l.failureSink = f } }
func New(store Store, limits Limits, options ...Option) (*Log, error) {
	if store == nil {
		return nil, errors.New("event store is required")
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	w, cw := context.WithCancel(context.Background())
	r, cr := context.WithCancel(context.Background())
	l := &Log{store: store, limits: limits, jobs: make(map[*publication]struct{}), producers: make(chan struct{}, limits.Entries+1), wake: make(chan struct{}), done: make(chan struct{}), writeCtx: w, cancelWrite: cw, writeTimeout: 15 * time.Second, reads: admission.New(r), cancelReads: cr, disposeGate: make(chan struct{}, 1)}
	for _, option := range options {
		option(l)
	}
	go l.run()
	return l, nil
}
func (l *Log) signal() { close(l.wake); l.wake = make(chan struct{}) }

// withWriteBudget runs one backend write under the write timeout. Expiry fails
// the log immediately, without waiting for the blocked write to return.
func (l *Log) withWriteBudget(op func(context.Context) error) error {
	run, cancel := context.WithTimeout(l.writeCtx, l.writeTimeout)
	defer cancel()
	defer context.AfterFunc(run, func() { l.Fail(run.Err()) })()
	return op(run)
}
func (l *Log) fail(err error) {
	if err == nil || l.err != nil {
		return
	}
	failure := errors.Join(ErrCapture, err)
	if head := l.store.Fail(failure); head.State == Sealed {
		return
	}
	l.err = failure
	l.cancelWrite()
	for j := range l.jobs {
		j.err = l.err
		close(j.done)
		delete(l.jobs, j)
	}
	l.queue = nil
	l.pending = 0
	l.bytes = 0
	l.signal()
	if l.failureSink != nil {
		go l.failureSink(l.err)
	}
}
func (l *Log) Fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
		return
	default:
	}
	l.fail(err)
}

// Publish is the short form of PublishContext, with the same readable-write acknowledgment.
func (l *Log) Publish(d Data) error { _, err := l.PublishContext(context.Background(), d); return err }
func (l *Log) PublishContext(ctx context.Context, d Data) (Cursor, error) {
	if err := ctx.Err(); err != nil {
		return Cursor{}, err
	}
	if err := d.Validate(); err != nil {
		l.Fail(err)
		return Cursor{}, err
	}
	if d.Time.IsZero() {
		d.Time = time.Now().UTC()
	}
	size := d.Size()
	if size > MaxRecordBytes || size > l.limits.Bytes {
		return Cursor{}, ErrRecordSize
	}
	select {
	case l.producers <- struct{}{}:
		defer func() { <-l.producers }()
	default:
		return Cursor{}, ErrQuota
	}
	l.mu.Lock()
	for {
		if l.err != nil {
			err := l.err
			l.mu.Unlock()
			return Cursor{}, err
		}
		if l.finishing {
			l.mu.Unlock()
			return Cursor{}, ErrSealed
		}
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return Cursor{}, err
		}
		if l.pending < l.limits.Entries && l.bytes+size <= l.limits.Bytes {
			break
		}
		wake := l.wake
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return Cursor{}, ctx.Err()
		case <-wake:
		}
		l.mu.Lock()
	}
	j := &publication{data: d.Clone(), done: make(chan struct{})}
	l.queue = append(l.queue, j)
	l.jobs[j] = struct{}{}
	l.pending++
	l.bytes += size
	l.accepted++
	l.signal()
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return Cursor{}, ctx.Err()
	case <-j.done:
		return j.cursor, j.err
	}
}
func (l *Log) run() {
	defer close(l.done)
	defer l.cancelWrite()
	for {
		l.mu.Lock()
		if l.err != nil {
			l.mu.Unlock()
			return
		}
		if len(l.queue) > 0 {
			j := l.queue[0]
			l.queue[0] = nil
			l.queue = l.queue[1:]
			l.mu.Unlock()
			var e Event
			err := l.withWriteBudget(func(run context.Context) (err error) {
				e, err = l.store.Append(run, j.data)
				return err
			})
			l.mu.Lock()
			if err != nil {
				l.fail(err)
			}
			if _, ok := l.jobs[j]; ok {
				j.cursor = e.Cursor()
				j.err = err
				close(j.done)
				delete(l.jobs, j)
				l.completed++
				l.pending--
				l.bytes -= j.data.Size()
				l.signal()
			}
			l.mu.Unlock()
			continue
		}
		if l.finishing {
			o := l.outcome
			l.mu.Unlock()
			err := l.withWriteBudget(func(run context.Context) error { return l.store.Seal(run, o) })
			l.mu.Lock()
			if err != nil {
				l.fail(err)
			}
			l.signal()
			l.mu.Unlock()
			return
		}
		wake := l.wake
		l.mu.Unlock()
		<-wake
	}
}

// Flush is a barrier for the publications admitted before this call. It does not sync disk.
func (l *Log) Flush(ctx context.Context) error {
	l.mu.Lock()
	target := l.accepted
	for {
		if l.err != nil {
			err := l.err
			l.mu.Unlock()
			return err
		}
		if l.completed >= target {
			l.mu.Unlock()
			return nil
		}
		wake := l.wake
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
		l.mu.Lock()
	}
}

// Finish permanently stops publication and seals after earlier writes. An
// ambiguous append/seal failure is latched and is never automatically retried.
func (l *Log) Finish(ctx context.Context, o Outcome) error {
	l.mu.Lock()
	if !l.finishing {
		l.finishing = true
		l.outcome = o
		l.signal()
	} else if l.outcome != o {
		l.mu.Unlock()
		return errors.New("conflicting shutdown outcome")
	}
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.done:
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}
func (l *Log) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := Status{Disposed: l.disposed}
	if l.err != nil {
		s.CaptureError = l.err.Error()
	}
	select {
	case <-l.done:
		s.Sealed = l.finishing && l.err == nil
	default:
	}
	return s
}
func (l *Log) Read(ctx context.Context, q Query) (Page, error) {
	run, done, err := l.reads.Begin(ctx)
	if err != nil {
		return Page{}, readError(err)
	}
	defer done()
	return l.store.Read(run, q)
}

// Dispose seals read admission, cancels/joins admitted reads, then closes storage.
// Finish must have completed first. Repeated calls can finish interrupted cleanup.
func (l *Log) Dispose(ctx context.Context) error {
	select {
	case <-l.done:
	default:
		return errors.New("event publication is still active")
	}
	l.mu.Lock()
	l.reads.Seal()
	l.cancelReads()
	l.signal()
	l.mu.Unlock()
	if err := l.reads.Wait(ctx); err != nil {
		return err
	}
	select {
	case l.disposeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l.disposeGate }()
	l.mu.Lock()
	disposed := l.disposed
	l.mu.Unlock()
	if disposed {
		return nil
	}
	if err := l.store.Close(ctx); err != nil {
		return err
	}
	l.mu.Lock()
	l.disposed = true
	l.signal()
	l.mu.Unlock()
	return nil
}

type Subscription struct {
	log      *Log
	after    uint64
	ctx      context.Context
	cancel   context.CancelFunc
	detached chan struct{}
	once     sync.Once
}

func (l *Log) Subscribe(ctx context.Context, after Cursor) (*Subscription, error) {
	head, err := l.Head(ctx)
	if err != nil {
		return nil, err
	}
	if after == (Cursor{}) {
		after.Session = head.Cursor.Session
	}
	if after.Session != head.Cursor.Session {
		return nil, ErrSession
	}
	if after.Sequence > head.Cursor.Sequence {
		return nil, ErrFuture
	}
	run, cancel := context.WithCancel(ctx)
	return &Subscription{log: l, after: after.Sequence, detached: make(chan struct{}), ctx: run, cancel: cancel}, nil
}
func (s *Subscription) Close() { s.once.Do(func() { close(s.detached); s.cancel() }) }

// Next supports one reader; cancellation does not advance the exclusive cursor.
func (s *Subscription) Next(ctx context.Context) (Event, error) {
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	for {
		if s.ctx.Err() != nil {
			return Event{}, ErrDetached
		}
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		select {
		case <-s.detached:
			return Event{}, ErrDetached
		default:
		}
		l := s.log
		p, err := l.Read(run, Query{After: s.after, Limit: 1})
		select {
		case <-s.detached:
			return Event{}, ErrDetached
		default:
		}
		if err != nil {
			return Event{}, err
		}
		// Detaching during a backend read must not consume the cursor.
		select {
		case <-s.detached:
			return Event{}, ErrDetached
		default:
		}
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		if len(p.Events) > 0 {
			s.after = p.Next
			return p.Events[0], nil
		}
		if p.Head.State == Failed {
			return Event{}, fmt.Errorf("%w: %s", ErrCapture, p.Head.Failure.Message)
		}
		if p.Sealed {
			return Event{}, io.EOF
		}
		waitCtx, done, err := l.reads.Begin(run)
		if err != nil {
			return Event{}, readError(err)
		}
		_, err = l.store.Wait(waitCtx, Cursor{Session: p.Head.Cursor.Session, Sequence: s.after})
		done()
		if err != nil {
			select {
			case <-s.detached:
				return Event{}, ErrDetached
			default:
			}
			return Event{}, err
		}
	}
}

func (l *Log) Head(ctx context.Context) (Head, error) {
	run, done, err := l.reads.Begin(ctx)
	if err != nil {
		return Head{}, readError(err)
	}
	defer done()
	return l.store.Head(run)
}
func (l *Log) Wait(ctx context.Context, after Cursor) (Head, error) {
	run, done, err := l.reads.Begin(ctx)
	if err != nil {
		return Head{}, readError(err)
	}
	defer done()
	return l.store.Wait(run, after)
}

func readError(err error) error {
	if errors.Is(err, admission.ErrClosed) {
		return ErrDisposed
	}
	return err
}
