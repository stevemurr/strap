package eventlog

import (
	"context"
	"errors"
	"sync"
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
func (s *storeState) head() Head {
	h := Head{Cursor: Cursor{s.session, s.latest}, State: Writable}
	if s.failed != nil {
		h.State = Failed
		h.Failure = &Problem{Code: "storage_failed", Message: s.failed.Error()}
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
func (s *storeState) Fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil && !s.disposed && s.outcome == nil {
		s.failed = errors.Join(ErrCapture, err)
		s.signal()
	}
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
