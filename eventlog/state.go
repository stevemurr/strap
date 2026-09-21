package eventlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

type Cursor struct {
	Session  string `json:"session"`
	Sequence uint64 `json:"sequence"`
}

type StoreState string

const (
	Writable StoreState = "writable"
	Sealed   StoreState = "sealed"
	Failed   StoreState = "failed"
)

type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Head struct {
	Cursor  Cursor     `json:"cursor"`
	State   StoreState `json:"state"`
	Failure *Problem   `json:"failure,omitempty"`
}

var (
	ErrQuota      = errors.New("event storage quota exceeded")
	ErrRecordSize = errors.New("event record exceeds size limit")
	ErrPageSize   = errors.New("event exceeds page byte budget")
	ErrSession    = errors.New("event cursor belongs to another session")
)

const MaxRecordBytes = 1 << 20

// storeState protects visibility, independently of backend I/O. A late write
// cannot become visible after Fail has frozen the accepted prefix.
type storeState struct {
	mu       sync.Mutex
	session  string
	latest   uint64
	outcome  *Outcome
	failed   error
	disposed bool
	wake     chan struct{}
}

func newState(session string) *storeState {
	return &storeState{session: session, wake: make(chan struct{})}
}
func (s *storeState) signal() { close(s.wake); s.wake = make(chan struct{}) }
func (s *storeState) writable() error {
	if s.disposed {
		return ErrDisposed
	}
	if s.failed != nil {
		return s.failed
	}
	if s.outcome != nil {
		return ErrSealed
	}
	return nil
}

// page starts a Read at q.After with the store's current position. The caller
// holds s.mu.
func (s *storeState) page(q Query) (Page, error) {
	p := Page{Latest: s.latest, Next: q.After, Sealed: s.outcome != nil, Head: s.head()}
	if s.latest > 0 {
		p.Earliest = 1
	}
	if s.outcome != nil {
		o := *s.outcome
		p.Outcome = &o
	}
	return p, cursor(q, p.Earliest, p.Latest)
}

// overBudget reports whether appending e would exceed q.MaxBytes after used
// bytes. A first event that does not fit is an error rather than an empty page.
func overBudget(p Page, e Event, used int, q Query) (bool, error) {
	if q.MaxBytes <= 0 || used+e.Size() <= q.MaxBytes {
		return false, nil
	}
	if len(p.Events) == 0 {
		return true, &PageBudgetError{Required: e.Size(), Budget: q.MaxBytes}
	}
	return true, nil
}

func (s *storeState) head() Head {
	h := Head{Cursor: Cursor{s.session, s.latest}, State: Writable}
	if s.failed != nil {
		h.State = Failed
		h.Failure = &Problem{Code: "storage_failed", Message: Summary(s.failed.Error())}
	} else if s.outcome != nil {
		h.State = Sealed
	}
	return h
}
func (s *storeState) Head(ctx context.Context) (Head, error) {
	if err := ctx.Err(); err != nil {
		return Head{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disposed {
		return Head{}, ErrDisposed
	}
	return s.head(), nil
}
func (s *storeState) Fail(err error) Head {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && s.failed == nil && !s.disposed && s.outcome == nil {
		s.failed = errors.Join(ErrCapture, err)
		s.signal()
	}
	return s.head()
}
func (s *storeState) Wait(ctx context.Context, after Cursor) (Head, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Head{}, err
		}
		s.mu.Lock()
		if s.disposed {
			s.mu.Unlock()
			return Head{}, ErrDisposed
		}
		if after.Session != "" && after.Session != s.session || after.Session == "" && after.Sequence != 0 {
			s.mu.Unlock()
			return Head{}, ErrSession
		}
		if after.Sequence > s.latest {
			s.mu.Unlock()
			return Head{}, ErrFuture
		}
		h, wake := s.head(), s.wake
		s.mu.Unlock()
		if h.Cursor.Sequence > after.Sequence || h.State != Writable {
			return h, nil
		}
		select {
		case <-ctx.Done():
			return Head{}, ctx.Err()
		case <-wake:
		}
	}
}

// Summary bounds control-plane error text; detailed facts remain in the log.
func Summary(s string) string {
	if len(s) <= 4096 {
		return s
	}
	n := 4093
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// PageBudgetError tells a finite reader how large the next whole record is.
type PageBudgetError struct {
	Required int
	Budget   int
}

func (e *PageBudgetError) Error() string {
	return fmt.Sprintf("%v: need %d bytes, budget %d", ErrPageSize, e.Required, e.Budget)
}
func (e *PageBudgetError) Unwrap() error { return ErrPageSize }
