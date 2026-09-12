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

// Log owns the single publisher and shared wakeup generation. Queue limits bound
// admitted payloads, including the write in progress; observers have no queues.
type Log struct {
	store               Store
	limits              Limits
	mu                  sync.Mutex
	queue               []Data
	pending, bytes      int
	accepted, completed uint64
	omitted             uint64
	err                 error
	finishing           bool
	outcome             Outcome
	wake                chan struct{}
	done                chan struct{}
	writeCtx            context.Context
	cancelWrite         context.CancelFunc
	reads               *admission.Gate
	cancelReads         context.CancelFunc
	disposeGate         chan struct{}
	disposed            bool
}
type Status struct {
	CaptureError string `json:"capture_error,omitempty"`
	Omitted      uint64 `json:"omitted"`
	Sealed       bool   `json:"sealed"`
	Disposed     bool   `json:"disposed"`
}

func New(store Store, limits Limits) (*Log, error) {
	if store == nil {
		return nil, errors.New("event store is required")
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	w, cw := context.WithCancel(context.Background())
	r, cr := context.WithCancel(context.Background())
	l := &Log{store: store, limits: limits, wake: make(chan struct{}), done: make(chan struct{}), writeCtx: w, cancelWrite: cw, reads: admission.New(r), cancelReads: cr, disposeGate: make(chan struct{}, 1)}
	go l.run()
	return l, nil
}
func (l *Log) signal() { close(l.wake); l.wake = make(chan struct{}) }
func (l *Log) fail(err error) {
	if l.err == nil {
		l.err = fmt.Errorf("%w: %v", ErrCapture, err)
		l.cancelWrite()
		l.signal()
	}
}
func (l *Log) Fail(err error) { l.mu.Lock(); defer l.mu.Unlock(); l.fail(err) }
func (l *Log) Publish(d Data) error {
	if err := d.Validate(); err != nil {
		l.Fail(err)
		return err
	}
	if d.Time.IsZero() {
		d.Time = time.Now().UTC()
	}
	d, omitted := Fit(d, l.limits.Bytes)
	d = d.Clone()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.finishing {
		return ErrSealed
	}
	if l.pending >= l.limits.Entries || l.bytes+d.Size() > l.limits.Bytes {
		l.fail(errors.New("publication queue capacity exceeded"))
		return l.err
	}
	l.queue = append(l.queue, d)
	l.pending++
	l.bytes += d.Size()
	l.accepted++
	if omitted {
		l.omitted++
	}
	l.signal()
	return nil
}
func (l *Log) run() {
	defer close(l.done)
	defer l.cancelWrite()
	for {
		l.mu.Lock()
		if l.err != nil {
			l.queue = nil
			l.mu.Unlock()
			return
		}
		if len(l.queue) > 0 {
			d := l.queue[0]
			l.queue[0] = Data{}
			l.queue = l.queue[1:]
			l.mu.Unlock()
			e, err := l.store.Append(l.writeCtx, d)
			l.mu.Lock()
			if err != nil {
				l.fail(err)
				l.mu.Unlock()
				continue
			}
			if e.Kind == "omitted" && d.Kind != "omitted" {
				l.omitted++
			}
			l.completed++
			l.pending--
			l.bytes -= d.Size()
			l.signal()
			l.mu.Unlock()
			continue
		}
		if l.finishing {
			o := l.outcome
			o.Omitted = l.omitted
			l.mu.Unlock()
			err := l.store.Seal(l.writeCtx, o)
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
	s := Status{Omitted: l.omitted, Disposed: l.disposed}
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
		if errors.Is(err, admission.ErrClosed) {
			err = ErrDisposed
		}
		return Page{}, err
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

func (l *Log) Subscribe(after uint64) *Subscription {
	ctx, cancel := context.WithCancel(context.Background())
	return &Subscription{log: l, after: after, detached: make(chan struct{}), ctx: ctx, cancel: cancel}
}
func (s *Subscription) Close() { s.once.Do(func() { close(s.detached); s.cancel() }) }

// Next supports one reader; cancellation does not advance the exclusive cursor.
func (s *Subscription) Next(ctx context.Context) (Event, error) {
	run, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	for {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		select {
		case <-s.detached:
			return Event{}, ErrDetached
		default:
		}
		l := s.log
		l.mu.Lock()
		wake, err := l.wake, l.err
		l.mu.Unlock()
		if err != nil {
			return Event{}, err
		}
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
		if p.Sealed {
			return Event{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-s.detached:
			return Event{}, ErrDetached
		case <-wake:
		}
	}
}
