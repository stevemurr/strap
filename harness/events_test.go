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
func TestCaptureFailureDoesNotDisableCommandsOrHideCloseFailure(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: idle{}, EventStore: func(id string) (eventlog.Store, error) {
		m, err := eventlog.NewMemory(id, eventlog.Limits{Entries: 10, Bytes: 4096})
		return failedStore{m}, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FlushEvents(context.Background()); !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(err)
	}
	if _, err := s.Send(s.Root(), "still admitted"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(err)
	}
	if s.State() != harness.Closed || s.Capture().CaptureError == "" {
		t.Fatal(s.State(), s.Capture())
	}
	if err := s.Dispose(context.Background()); !errors.Is(err, eventlog.ErrCapture) {
		t.Fatal(err)
	}
	if !s.Capture().Disposed {
		t.Fatal("capture failure leaked storage")
	}
}
