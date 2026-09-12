package eventlog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
)

func TestMemoryQuotaPreservesAcceptedPrefix(t *testing.T) {
	s := memory(t, 2, 4096)
	defer s.Close(ctx)
	for i := 0; i < 2; i++ {
		if _, err := s.Append(ctx, data(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Append(ctx, data(2)); err == nil {
		t.Fatal("quota accepted a third record")
	}
	page, err := s.Read(ctx, eventlog.Query{Limit: 10})
	if err != nil || len(page.Events) != 2 || page.Events[0].Sequence != 1 {
		t.Fatalf("accepted prefix lost: %+v %v", page, err)
	}
}

func TestStoreWaitObservesAppendAndFailure(t *testing.T) {
	s := memory(t, 10, 4096)
	defer s.Close(ctx)
	first, err := s.Append(ctx, data(1))
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	h, err := s.Wait(run, eventlog.Cursor{Session: "test"})
	if err != nil || h.Cursor.Sequence != first.Sequence {
		t.Fatal(h, err)
	}
	s.Fail(errors.New("publication failed"))
	h, err = s.Wait(run, h.Cursor)
	if err != nil || h.State != eventlog.Failed {
		t.Fatal(h, err)
	}
	if _, err = s.Append(ctx, data(2)); err == nil {
		t.Fatal("append after failure")
	}
	page, err := s.Read(ctx, eventlog.Query{Limit: 10})
	if err != nil || len(page.Events) != 1 {
		t.Fatal(page, err)
	}
}

// This backend deliberately ignores cancellation during physical I/O.
type stuckStore struct {
	eventlog.Store
	entered, release chan struct{}
}

func (s *stuckStore) Append(_ context.Context, d eventlog.Data) (eventlog.Event, error) {
	close(s.entered)
	<-s.release
	return s.Store.Append(context.Background(), d)
}
func TestFailureWakesPublicationBeforePhysicalIOCompletes(t *testing.T) {
	s := &stuckStore{Store: memory(t, 10, 4096), entered: make(chan struct{}), release: make(chan struct{})}
	l, err := eventlog.New(s, eventlog.Limits{Entries: 1, Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- l.Publish(data(1)) }()
	<-s.entered
	l.Fail(errors.New("recovery unavailable"))
	select {
	case err := <-result:
		if !errors.Is(err, eventlog.ErrCapture) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("publication remained blocked")
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := l.Finish(short, eventlog.Outcome{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(s.release)
	if err := l.Finish(ctx, eventlog.Outcome{}); !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(err)
	}
	p, err := l.Read(ctx, eventlog.Query{Limit: 10})
	if err != nil || p.Latest != 0 || p.Head.State != eventlog.Failed {
		t.Fatal(p, err)
	}
	if err := l.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
}
