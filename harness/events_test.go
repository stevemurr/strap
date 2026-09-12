package harness_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
)

func TestSessionSubscribersSeeStartupAndFinalEvents(t *testing.T) {
	s := newLifecycleSession(t, context.Background(), idle{})
	a, b := s.Subscribe(0), s.Subscribe(0)
	defer a.Close()
	defer b.Close()
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, exited, terminal := false, false, false
	for {
		e, err := a.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		f, err := b.Next(context.Background())
		if err != nil || f.Sequence != e.Sequence || string(f.Payload) != string(e.Payload) {
			t.Fatal(e, f, err)
		}
		switch e.Kind {
		case "session_started":
			started = true
		case "agent_exited":
			exited = true
		case "session_closed":
			terminal = true
		}
	}
	if !started || !exited || !terminal {
		t.Fatal(started, exited, terminal)
	}
	info, err := s.InspectAgent(s.Root(), conversation.InspectOptions{})
	if err != nil || info.StateRevision < 2 {
		t.Fatal(info, err)
	}
	if err := s.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Events(context.Background(), eventlog.Query{Limit: 1}); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
}

type failedStore struct{ eventlog.Store }

func (f failedStore) Append(context.Context, eventlog.Data) (eventlog.Event, error) {
	return eventlog.Event{}, errors.New("storage unavailable")
}
func TestCaptureFailureRejectsStartupAndReleasesStorage(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	var store eventlog.Store
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: idle{}, EventStore: func(id string) (eventlog.Store, error) {
		m, e := eventlog.NewMemory(id, eventlog.Limits{})
		store = m
		return failedStore{m}, e
	}})
	if s != nil || !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(s, err)
	}
	if _, err := store.Head(context.Background()); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal("startup leaked storage", err)
	}
}
