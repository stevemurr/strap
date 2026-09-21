package harness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
)

// startupFails requires session construction to fail, and requires it to leave
// nothing behind for the caller to clean up.
func startupFails(t *testing.T, cfg harness.Config, deps harness.Dependencies, because string) error {
	t.Helper()
	s, err := harness.New(context.Background(), cfg, deps)
	if err == nil {
		_ = s.Dispose(context.Background())
		t.Fatal("session started despite", because)
	}
	if s != nil {
		t.Fatal("failed startup returned a session:", because)
	}
	return err
}

func startupConfig() harness.Config {
	c := harness.DefaultConfig()
	c.LocalTools, c.Web = false, nil
	return c
}

// A session refuses to start on a configuration it cannot honor, rather than
// starting in a state the caller did not ask for.
func TestSessionStartupValidatesConfiguration(t *testing.T) {
	memory := func(id string) (eventlog.Store, error) {
		return eventlog.NewMemory(id, eventlog.Limits{Entries: 8, Bytes: 1 << 20})
	}

	both := startupConfig()
	both.Events.JSONLPath = filepath.Join(t.TempDir(), "trace.jsonl")
	startupFails(t, both, harness.Dependencies{Provider: textResponse("ready"), EventStore: memory}, "two conflicting event stores")

	retention := startupConfig()
	retention.Events.Retention = eventlog.Limits{Entries: 0, Bytes: 1}
	startupFails(t, retention, harness.Dependencies{Provider: textResponse("ready")}, "unusable retention limits")

	queue := startupConfig()
	queue.Events.Queue = eventlog.Limits{Entries: -1, Bytes: 1}
	startupFails(t, queue, harness.Dependencies{Provider: textResponse("ready")}, "unusable queue limits")

	telemetry := startupConfig()
	telemetry.Telemetry.Concurrency = 1000
	startupFails(t, telemetry, harness.Dependencies{Provider: textResponse("ready")}, "out-of-range telemetry concurrency")

	telemetry = startupConfig()
	telemetry.Telemetry.Timeout = -time.Second
	startupFails(t, telemetry, harness.Dependencies{Provider: textResponse("ready")}, "a negative telemetry timeout")

	unwritable := startupConfig()
	unwritable.Events.JSONLPath = filepath.Join(t.TempDir(), "missing-dir", "trace.jsonl")
	startupFails(t, unwritable, harness.Dependencies{Provider: textResponse("ready")}, "a trace path that cannot be created")

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := harness.New(cancelled, startupConfig(), harness.Dependencies{Provider: textResponse("ready")}); !errors.Is(err, context.Canceled) {
		t.Fatal("startup ignored a cancelled context", err)
	}
}

// Dependencies are checked before anything is built from them.
func TestSessionStartupValidatesDependencies(t *testing.T) {
	err := startupFails(t, startupConfig(), harness.Dependencies{Provider: textResponse("ready"), Resources: []harness.OwnedResource{{Name: "absent"}}}, "a nil owned resource")
	if !strings.Contains(err.Error(), "absent") {
		t.Fatal("the failure did not name the offending resource", err)
	}
	startupFails(t, startupConfig(), harness.Dependencies{Provider: textResponse("ready"), EventStore: func(string) (eventlog.Store, error) {
		return nil, nil
	}}, "an event store factory that produced nothing")
	startupFails(t, startupConfig(), harness.Dependencies{Provider: textResponse("ready"), EventStore: func(string) (eventlog.Store, error) {
		return nil, errors.New("storage offline")
	}}, "an event store factory that failed")
}

// The event store must be empty and bound to this session; a reused store would
// interleave two sessions' histories.
func TestSessionStartupRequiresAnEmptyStoreForThisSession(t *testing.T) {
	ctx := context.Background()
	startupFails(t, startupConfig(), harness.Dependencies{Provider: textResponse("ready"), EventStore: func(string) (eventlog.Store, error) {
		return eventlog.NewMemory("someone-else", eventlog.Limits{Entries: 8, Bytes: 1 << 20})
	}}, "a store bound to another session")

	startupFails(t, startupConfig(), harness.Dependencies{Provider: textResponse("ready"), EventStore: func(id string) (eventlog.Store, error) {
		store, err := eventlog.NewMemory(id, eventlog.Limits{Entries: 8, Bytes: 1 << 20})
		if err != nil {
			return nil, err
		}
		if _, err := store.Append(ctx, eventlog.Data{Kind: "note", Payload: []byte(`{}`)}); err != nil {
			return nil, err
		}
		return store, nil
	}}, "a store that already holds records")

	startupFails(t, startupConfig(), harness.Dependencies{Provider: textResponse("ready"), EventStore: func(id string) (eventlog.Store, error) {
		store, err := eventlog.NewMemory(id, eventlog.Limits{Entries: 8, Bytes: 1 << 20})
		if err != nil {
			return nil, err
		}
		return store, store.Seal(ctx, eventlog.Outcome{Reason: "requested"})
	}}, "a store that is already sealed")
}

// A trace path given to a session is created by the session and holds the
// session's own history.
func TestSessionWritesItsConfiguredTrace(t *testing.T) {
	ctx := context.Background()
	cfg := startupConfig()
	cfg.Dir = t.TempDir()
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(s.Root(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushEvents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"session_started"`) || !strings.Contains(string(body), s.ID()) {
		t.Fatal("trace does not describe this session")
	}
	if err := s.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	// Disposal releases history; later reads say so rather than returning nothing.
	if _, err := s.Events(ctx, eventlog.Query{Limit: 10, MaxBytes: 1 << 20}); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
	if _, err := s.NextEvent(ctx); err == nil {
		t.Fatal("read an event from a disposed session")
	}
}
