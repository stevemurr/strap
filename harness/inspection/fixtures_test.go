package inspection_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/work"
)

// sessionLog opens a JSONL store holding one started session and returns its path.
func sessionLog(t *testing.T, id string) (*eventlog.JSONL, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	log, err := eventlog.NewJSONL(path, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close(ctx) })
	if _, err = log.Append(ctx, eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"` + id + `"}`)}); err != nil {
		t.Fatal(err)
	}
	return log, path
}
func appender(t *testing.T, store eventlog.Store) func(conversation.Event) eventlog.Cursor {
	return func(e conversation.Event) eventlog.Cursor {
		t.Helper()
		d, err := eventcodec.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		r, err := store.Append(context.Background(), d)
		if err != nil {
			t.Fatal(err)
		}
		return r.Cursor()
	}
}

// reportingStore records every work event to log; last, when set, tracks the newest cursor.
func reportingStore(log eventlog.Store, last *eventlog.Cursor) *work.Store {
	return work.New(work.WithReporter(work.ReporterFunc(func(ctx context.Context, e work.Event) error {
		d, err := eventcodec.EncodeEvent(conversation.WorkEvent{Event: e})
		if err != nil {
			return err
		}
		r, err := log.Append(ctx, d)
		if err == nil && last != nil {
			*last = r.Cursor()
		}
		return err
	})))
}

// awaitIdle drains events until the root returns to Idle.
func awaitIdle(t *testing.T, s *harness.Session) {
	t.Helper()
	wait, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(wait)
		if err != nil {
			t.Fatal("session never settled", err)
		}
		if c, ok := e.(conversation.AgentStateChanged); ok && c.Agent == s.Root() && c.State == agent.Idle {
			return
		}
	}
}
