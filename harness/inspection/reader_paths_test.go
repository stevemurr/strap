package inspection_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/work"
)

// archive writes a JSONL trace verbatim and opens it for inspection.
func archive(t *testing.T, lines ...string) *eventlog.JSONL {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.OpenJSONL(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store
}

func traceLine(session string, sequence uint64, kind, payload string) string {
	raw, err := json.Marshal(map[string]any{"schema": eventlog.SchemaVersion, "session": session, "sequence": sequence, "kind": kind, "payload": json.RawMessage(payload)})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// A reader needs a source, and refuses to serve a prefix from a different
// session than the one it opened.
func TestReaderRequiresASourceAndItsOwnSession(t *testing.T) {
	ctx := context.Background()
	if _, err := inspection.New(ctx, nil); err == nil {
		t.Fatal("built a reader with no source")
	}
	store := archive(t,
		traceLine("s", 1, "session_started", `{"id":"s"}`),
		traceLine("s", 2, "session_closed", `{"reason":"requested"}`),
	)
	reader, err := inspection.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.At(ctx, eventlog.Cursor{Session: "other", Sequence: 1}); !errors.Is(err, eventlog.ErrSession) {
		t.Fatal("served a prefix from another session", err)
	}
	view, err := reader.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.ReadRecord(ctx, eventlog.Cursor{Session: "other", Sequence: 1}); !errors.Is(err, eventlog.ErrSession) {
		t.Fatal("read a record from another session", err)
	}
	if _, err := view.ReadRecord(ctx, eventlog.Cursor{Session: "s", Sequence: 99}); err == nil {
		t.Fatal("read a record past the end of the trace")
	}
	// A prefix beyond the trace cannot be pinned.
	if _, err := reader.At(ctx, eventlog.Cursor{Session: "s", Sequence: 99}); err == nil {
		t.Fatal("pinned a prefix the source cannot reach")
	}
	if err := reader.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.At(ctx, eventlog.Cursor{}); err == nil {
		t.Fatal("served a prefix from a closed reader")
	}
	if _, err := reader.Head(ctx); err == nil {
		t.Fatal("read the head of a closed reader")
	}
}

// An archive that is well-formed as a file but incoherent as a session fails
// when it is projected, naming the record that could not be applied.
func TestReaderReportsUnprojectableArchives(t *testing.T) {
	ctx := context.Background()
	// An output that references an agent the trace never started.
	store := archive(t,
		traceLine("s", 1, "session_started", `{"id":"s"}`),
		traceLine("s", 2, "output_started", `{"output":{"agent":"ghost","call":1},"context_revision":0,"started_at":"2024-01-01T00:00:00Z"}`),
	)
	reader, err := inspection.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	_, err = reader.At(ctx, eventlog.Cursor{})
	if err == nil {
		t.Fatal("projected an incoherent archive")
	}
	if !strings.Contains(err.Error(), "record 2") {
		t.Fatal("the failure did not name the offending record", err)
	}
}

// A work listing cursor encodes the prefix it was taken at, so a forged or
// damaged cursor is refused rather than silently resetting the listing.
func TestWorkCursorValidation(t *testing.T) {
	f := tracedSession(t)
	forge := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	cases := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"not JSON", base64.RawURLEncoding.EncodeToString([]byte("{"))},
		{"unknown field", forge(map[string]any{"session": "s", "prefix": 2, "sequence": 1, "id": "w-1", "surprise": true})},
		{"no session", forge(map[string]any{"prefix": 2, "sequence": 1, "id": "w-1"})},
		{"no prefix", forge(map[string]any{"session": "s", "sequence": 1, "id": "w-1"})},
		{"no work id", forge(map[string]any{"session": "s", "prefix": 2, "sequence": 1})},
		{"sequence past prefix", forge(map[string]any{"session": "s", "prefix": 1, "sequence": 2, "id": "w-1"})},
		{"cursor carrying its own cursor", forge(map[string]any{"session": "s", "prefix": 2, "sequence": 1, "id": "w-1", "filters": map[string]any{"cursor": "x"}})},
		{"cursor carrying a limit", forge(map[string]any{"session": "s", "prefix": 2, "sequence": 1, "id": "w-1", "filters": map[string]any{"limit": 5}})},
	}
	for _, tc := range cases {
		if w := f.get(t, "/work?cursor="+tc.cursor); w.Code != 400 {
			t.Fatal(tc.name, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/work?kind=teleportation", "/work?state=teleporting", "/work?limit=101"} {
		if w := f.get(t, path); w.Code != 400 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	// A cursor issued by the reader itself continues the listing.
	page := decode[work.ListPage](t, f.get(t, fmt.Sprintf("/work?actor=%s&limit=1", f.root)))
	if len(page.Items) != 1 {
		t.Fatal("limit did not bound the page", page)
	}
	if page.NextCursor != "" {
		if w := f.get(t, "/work?actor="+string(f.root)+"&cursor="+page.NextCursor); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// A listing on behalf of an agent that does not exist is refused.
	if w := f.get(t, "/work?actor=ghost"); w.Code != 404 && w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// A single work item is inspectable directly from a pinned view, and is subject
// to the same access rules as the listing.
func TestViewInspectWork(t *testing.T) {
	f := tracedSession(t)
	view, err := f.reader.At(f.ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := view.ListWork(f.ctx, f.root, work.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) == 0 {
		t.Fatal("the trace recorded no work")
	}
	id := listed.Items[0].ID
	got, err := view.InspectWork(f.ctx, f.root, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Work.ID != id {
		t.Fatal("inspection returned a different item", got.Work.ID, id)
	}
	if _, err := view.InspectWork(f.ctx, f.root, "w-missing"); err == nil {
		t.Fatal("inspected work that does not exist")
	}
	if _, err := view.InspectWork(f.ctx, "ghost", id); err == nil {
		t.Fatal("inspected work on behalf of an agent that does not exist")
	}
}
