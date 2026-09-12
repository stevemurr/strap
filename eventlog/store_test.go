package eventlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
)

var ctx = context.Background()

func data(n int) eventlog.Data {
	return eventlog.Data{Kind: "test", Payload: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))}
}
func memory(t *testing.T, entries, bytes int) *eventlog.Memory {
	t.Helper()
	m, err := eventlog.NewMemory("test", eventlog.Limits{Entries: entries, Bytes: bytes})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Shared with every writable backend: cursor, ownership, sealing, and disposal.
func storeContract(t *testing.T, newStore func() eventlog.Store) {
	t.Helper()
	s := newStore()
	defer s.Close(ctx)
	original := data(1)
	e, err := s.Append(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	original.Payload[5] = '9'
	e.Payload[5] = '8'
	p, err := s.Read(ctx, eventlog.Query{Limit: 10})
	if err != nil || string(p.Events[0].Payload) != `{"n":1}` {
		t.Fatal(p, err)
	}
	p.Events[0].Payload[5] = '7'
	p, err = s.Read(ctx, eventlog.Query{After: 1, Limit: 10})
	if err != nil || p.Next != 1 || len(p.Events) != 0 {
		t.Fatal(p, err)
	}
	if _, err = s.Read(ctx, eventlog.Query{After: 2, Limit: 1}); !errors.Is(err, eventlog.ErrFuture) {
		t.Fatal(err)
	}
	if _, err = s.Read(ctx, eventlog.Query{}); err == nil {
		t.Fatal("invalid page limit")
	}
	o := eventlog.Outcome{Error: "domain failure"}
	if err = s.Seal(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err = s.Seal(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err = s.Seal(ctx, eventlog.Outcome{}); err == nil {
		t.Fatal("conflicting seal accepted")
	}
	if _, err = s.Append(ctx, data(2)); !errors.Is(err, eventlog.ErrSealed) {
		t.Fatal(err)
	}
	p, err = s.Read(ctx, eventlog.Query{Limit: 10})
	if err != nil || len(p.Events) != 2 || !p.Sealed || p.Outcome.Error != o.Error || p.Events[1].Kind != "session_closed" {
		t.Fatal(p, err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(ctx, eventlog.Query{Limit: 1}); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
}
func TestMemoryContract(t *testing.T) {
	storeContract(t, func() eventlog.Store { return memory(t, 10, 4096) })
}
func TestRetentionAndOversizeAreExplicit(t *testing.T) {
	m := memory(t, 2, 4096)
	for i := 0; i < 3; i++ {
		if _, err := m.Append(ctx, data(i)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := m.Read(ctx, eventlog.Query{Limit: 10})
	if !errors.Is(err, eventlog.ErrExpired) || p.Earliest != 2 || p.Latest != 3 {
		t.Fatal(p, err)
	}
	raw, _ := json.Marshal(strings.Repeat("x", 5000))
	e, err := m.Append(ctx, eventlog.Data{Kind: "huge", Agent: "a", Payload: raw})
	if err != nil || e.Kind != "omitted" || e.Agent != "a" {
		t.Fatal(e, err)
	}
	if err = m.Seal(ctx, eventlog.Outcome{Omitted: 1}); err != nil {
		t.Fatal(err)
	}
	p, err = m.Read(ctx, eventlog.Query{After: 3, Limit: 10})
	if err != nil || len(p.Events) != 2 || p.Outcome.Omitted != 1 {
		t.Fatal(p, err)
	}
}
func logFor(t *testing.T, s eventlog.Store, entries int) *eventlog.Log {
	t.Helper()
	l, err := eventlog.New(s, eventlog.Limits{Entries: entries, Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = l.Finish(ctx, eventlog.Outcome{})
		if err := l.Dispose(ctx); err != nil {
			t.Error(err)
		}
	})
	return l
}
func TestIndependentReadersReconnectDetachAndEOF(t *testing.T) {
	l := logFor(t, memory(t, 100, 16384), 100)
	a, b := l.Subscribe(0), l.Subscribe(0)
	defer a.Close()
	defer b.Close()
	for i := 0; i < 5; i++ {
		if err := l.Publish(data(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		x, err := a.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		y, err := b.Next(ctx)
		if err != nil || !reflect.DeepEqual(x, y) {
			t.Fatal(x, y, err)
		}
	}
	waiting := make(chan error, 1)
	go func() { _, err := a.Next(ctx); waiting <- err }()
	a.Close()
	if err := <-waiting; !errors.Is(err, eventlog.ErrDetached) {
		t.Fatal(err)
	}
	c := l.Subscribe(5)
	defer c.Close()
	short, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Next(short); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := l.Publish(data(5)); err != nil {
		t.Fatal(err)
	}
	if x, err := c.Next(ctx); err != nil || x.Sequence != 6 {
		t.Fatal(x, err)
	}
	if err := l.Finish(ctx, eventlog.Outcome{}); err != nil {
		t.Fatal(err)
	}
	if x, err := c.Next(ctx); err != nil || x.Kind != "session_closed" {
		t.Fatal(x, err)
	}
	if _, err := c.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if err := l.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Next(ctx); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
}

type slowStore struct {
	eventlog.Store
	entered, release chan struct{}
	fail             bool
	calls            int
	mu               sync.Mutex
}

func (s *slowStore) Append(ctx context.Context, d eventlog.Data) (eventlog.Event, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	close(s.entered)
	select {
	case <-ctx.Done():
		return eventlog.Event{}, ctx.Err()
	case <-s.release:
	}
	if s.fail {
		return eventlog.Event{}, errors.New("disk full")
	}
	return s.Store.Append(ctx, d)
}
func TestSaturationAndStorageFailuresLatchWithoutRetry(t *testing.T) {
	for _, saturation := range []bool{true, false} {
		t.Run(fmt.Sprint(saturation), func(t *testing.T) {
			s := &slowStore{Store: memory(t, 10, 4096), entered: make(chan struct{}), release: make(chan struct{}), fail: !saturation}
			l := logFor(t, s, 1)
			sub := l.Subscribe(0)
			defer sub.Close()
			if err := l.Publish(data(1)); err != nil {
				t.Fatal(err)
			}
			<-s.entered
			if saturation {
				if err := l.Publish(data(2)); !errors.Is(err, eventlog.ErrCapture) {
					t.Fatal(err)
				}
			} else {
				close(s.release)
			}
			if err := l.Finish(ctx, eventlog.Outcome{}); !errors.Is(err, eventlog.ErrCapture) {
				t.Fatal(err)
			}
			if _, err := sub.Next(ctx); !errors.Is(err, eventlog.ErrCapture) {
				t.Fatal(err)
			}
			if err := l.Publish(data(3)); !errors.Is(err, eventlog.ErrCapture) {
				t.Fatal(err)
			}
			if err := l.Flush(ctx); !errors.Is(err, eventlog.ErrCapture) {
				t.Fatal(err)
			}
			if s.calls != 1 || l.Status().Sealed {
				t.Fatal(s.calls, l.Status())
			}
		})
	}
}

type readRace struct {
	eventlog.Store
	read, release chan struct{}
	once          sync.Once
}

func (s *readRace) Read(ctx context.Context, q eventlog.Query) (eventlog.Page, error) {
	p, err := s.Store.Read(ctx, q)
	s.once.Do(func() {
		close(s.read)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	return p, err
}
func TestReadAppendHandoffDoesNotLoseWake(t *testing.T) {
	s := &readRace{Store: memory(t, 10, 4096), read: make(chan struct{}), release: make(chan struct{})}
	l := logFor(t, s, 10)
	sub := l.Subscribe(0)
	defer sub.Close()
	result := make(chan error, 1)
	go func() { _, err := sub.Next(ctx); result <- err }()
	<-s.read
	if err := l.Publish(data(1)); err != nil {
		t.Fatal(err)
	}
	if err := l.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	close(s.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("lost append wakeup")
	}
}
func TestDetachCancelsBackendRead(t *testing.T) {
	s := &readRace{Store: memory(t, 10, 4096), read: make(chan struct{}), release: make(chan struct{})}
	l := logFor(t, s, 10)
	sub := l.Subscribe(0)
	result := make(chan error, 1)
	go func() { _, err := sub.Next(ctx); result <- err }()
	<-s.read
	sub.Close()
	select {
	case err := <-result:
		if !errors.Is(err, eventlog.ErrDetached) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("detached read still blocked")
	}
}

func TestDisposalCancelsReadAndRejectsNewReads(t *testing.T) {
	s := &readRace{Store: memory(t, 10, 4096), read: make(chan struct{}), release: make(chan struct{})}
	l := logFor(t, s, 10)
	result := make(chan error, 1)
	go func() { _, err := l.Read(ctx, eventlog.Query{Limit: 1}); result <- err }()
	<-s.read
	if err := l.Finish(ctx, eventlog.Outcome{}); err != nil {
		t.Fatal(err)
	}
	if err := l.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	<-result
	if _, err := l.Read(ctx, eventlog.Query{Limit: 1}); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
}
func TestByteRetentionAndCaptureOmissionMetadata(t *testing.T) {
	m := memory(t, 100, 4096)
	l := logFor(t, m, 10)
	raw, _ := json.Marshal(strings.Repeat("x", 6000))
	if err := l.Publish(eventlog.Data{Kind: "large", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if err := l.Finish(ctx, eventlog.Outcome{}); err != nil {
		t.Fatal(err)
	}
	p, err := l.Read(ctx, eventlog.Query{Limit: 10})
	if err != nil || p.Events[0].Kind != "omitted" || l.Status().Omitted != 1 || p.Outcome.Omitted != 1 {
		t.Fatal(p, err, l.Status())
	}
	m = memory(t, 100, 4096)
	raw, _ = json.Marshal(strings.Repeat("x", 2000))
	for i := 0; i < 3; i++ {
		if _, err = m.Append(ctx, eventlog.Data{Kind: "large", Payload: raw}); err != nil {
			t.Fatal(err)
		}
	}
	p, err = m.Read(ctx, eventlog.Query{Limit: 10})
	if !errors.Is(err, eventlog.ErrExpired) || p.Earliest != 3 {
		t.Fatal(p, err)
	}
}

func TestJSONLContract(t *testing.T) {
	storeContract(t, func() eventlog.Store {
		s, err := eventlog.NewJSONL(t.TempDir()+"/trace.jsonl", "test")
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestCaptureFailureUsesIndependentSinkOnce(t *testing.T) {
	s := &slowStore{Store: memory(t, 10, 4096), entered: make(chan struct{}), release: make(chan struct{}), fail: true}
	close(s.release)
	reported := make(chan error, 2)
	l, err := eventlog.New(s, eventlog.Limits{Entries: 10, Bytes: 4096}, eventlog.WithFailureSink(func(err error) { reported <- err }))
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Publish(data(1)); err != nil {
		t.Fatal(err)
	}
	if err = l.Finish(ctx, eventlog.Outcome{}); !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(err)
	}
	if err = <-reported; !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(err)
	}
	_ = l.Finish(ctx, eventlog.Outcome{})
	if len(reported) != 0 {
		t.Fatal("sink repeated")
	}
	if err = l.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
}
