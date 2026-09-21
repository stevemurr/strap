package eventlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
)

// writeTrace lays down a JSONL archive verbatim, including malformed content a
// writer would never produce.
func writeTrace(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func line(schema int, session string, sequence uint64, kind, payload string) string {
	e := map[string]any{"schema": schema, "session": session, "sequence": sequence, "kind": kind, "payload": json.RawMessage(payload), "time": time.Time{}}
	raw, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func started(session string) string {
	return line(eventlog.SchemaVersion, session, 1, "session_started", `{"id":"`+session+`"}`)
}
func closed(session string, sequence uint64) string {
	return line(eventlog.SchemaVersion, session, sequence, "session_closed", `{"reason":"requested"}`)
}

// A new trace file is exclusive and must be named by a session.
func TestNewJSONLRequiresAnIdentityAndAnUnusedPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := eventlog.NewJSONL(filepath.Join(dir, "a.jsonl"), ""); err == nil {
		t.Fatal("accepted a trace with no session identity")
	}
	path := filepath.Join(dir, "b.jsonl")
	store, err := eventlog.NewJSONL(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if _, err := eventlog.NewJSONL(path, "session"); err == nil {
		t.Fatal("reopened an existing trace for writing")
	}
	if _, err := eventlog.NewJSONL(filepath.Join(dir, "missing", "c.jsonl"), "session"); err == nil {
		t.Fatal("created a trace under a directory that does not exist")
	}
}

// Opening an archive replays and validates every record, so a damaged file is
// reported rather than silently truncated to its valid prefix.
func TestOpenJSONLRejectsDamagedArchives(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		lines []string
	}{
		{"not JSON", []string{"{"}},
		{"unsupported schema", []string{line(99, "s", 1, "session_started", `{"id":"s"}`)}},
		{"sequence gap", []string{started("s"), line(eventlog.SchemaVersion, "s", 7, "note", `{}`)}},
		{"empty session", []string{line(eventlog.SchemaVersion, "", 1, "session_started", `{}`)}},
		{"session changes mid-trace", []string{started("s"), line(eventlog.SchemaVersion, "other", 2, "note", `{}`)}},
		{"record after terminal", []string{started("s"), closed("s", 2), line(eventlog.SchemaVersion, "s", 3, "note", `{}`)}},
		{"invalid event data", []string{started("s"), line(eventlog.SchemaVersion, "s", 2, "", `{}`)}},
		{"unreadable outcome", []string{started("s"), line(eventlog.SchemaVersion, "s", 2, "session_closed", `"not-an-outcome"`)}},
	}
	for _, tc := range cases {
		if _, err := eventlog.OpenJSONL(ctx, writeTrace(t, tc.lines...)); err == nil {
			t.Fatal("opened a damaged archive:", tc.name)
		}
	}
	if _, err := eventlog.OpenJSONL(ctx, filepath.Join(t.TempDir(), "absent.jsonl")); err == nil {
		t.Fatal("opened an archive that does not exist")
	}
	// A cancelled open stops replaying rather than finishing the file.
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := eventlog.OpenJSONL(cancelled, writeTrace(t, started("s"), closed("s", 2))); err == nil {
		t.Fatal("replayed an archive under a cancelled context")
	}
}

// Trailing bytes after a terminal record mean the file was appended to after
// the session ended, which invalidates the whole archive.
func TestOpenJSONLRejectsBytesAfterTheTerminalRecord(t *testing.T) {
	path := writeTrace(t, started("s"), closed("s", 2))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("trailing"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := eventlog.OpenJSONL(context.Background(), path); err == nil {
		t.Fatal("opened an archive with unconfirmed trailing bytes")
	}
}

// An archive with no terminal record opens, but reports itself interrupted
// rather than presenting a partial trace as complete.
func TestOpenJSONLMarksAnInterruptedArchive(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.OpenJSONL(ctx, writeTrace(t, started("s"), line(eventlog.SchemaVersion, "s", 2, "note", `{}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	head, err := store.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.State != eventlog.Failed || head.Failure == nil || !strings.Contains(head.Failure.Message, "interrupted") {
		t.Fatal("interrupted archive did not report itself failed", head)
	}
	// A sealed archive reads cleanly through to its outcome.
	sealed, err := eventlog.OpenJSONL(ctx, writeTrace(t, started("s"), closed("s", 2)))
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close(ctx)
	h, err := sealed.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.State != eventlog.Sealed || h.Cursor.Sequence != 2 {
		t.Fatal(h)
	}
	page, err := sealed.Read(ctx, eventlog.Query{Limit: 10, MaxBytes: 1 << 20})
	if err != nil || len(page.Events) != 2 {
		t.Fatal(page, err)
	}
	// A read-only archive never accepts new records.
	if _, err := sealed.Append(ctx, eventlog.Data{Kind: "note", Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("appended to a read-only archive")
	}
}

// A page must fit its byte budget; a record larger than the whole budget is
// reported as such instead of returning an empty page forever.
func TestJSONLPageBudget(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.OpenJSONL(ctx, writeTrace(t, started("s"), closed("s", 2)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	if _, err := store.Read(ctx, eventlog.Query{Limit: 10, MaxBytes: 1}); err == nil {
		t.Fatal("returned a page for a budget no record can fit")
	}
	page, err := store.Read(ctx, eventlog.Query{Limit: 10, MaxBytes: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatal("budget did not stop the page after the first record", len(page.Events))
	}
}

// Log construction validates its dependencies and options up front.
func TestLogRequiresAStoreAndAPositiveWriteTimeout(t *testing.T) {
	if _, err := eventlog.New(nil, eventlog.Limits{Entries: 8, Bytes: 1 << 20}); err == nil {
		t.Fatal("built a log with no store")
	}
	store, err := eventlog.NewMemory("session", eventlog.Limits{Entries: 8, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventlog.New(store, eventlog.Limits{}); err == nil {
		t.Fatal("accepted limits that permit nothing")
	}
	log, err := eventlog.New(store, eventlog.Limits{Entries: 8, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Storage is only released once publication has finished.
	if err := log.Dispose(ctx); err == nil {
		t.Fatal("disposed a log while publication was still active")
	}
	if err := log.Finish(ctx, eventlog.Outcome{Reason: "requested"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
}

// A budget error names the record that did not fit, so a finite reader can
// enlarge its budget precisely rather than guessing.
func TestPageBudgetErrorDescribesTheRecord(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.OpenJSONL(ctx, writeTrace(t, started("s"), closed("s", 2)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	_, err = store.Read(ctx, eventlog.Query{Limit: 10, MaxBytes: 1})
	if err == nil {
		t.Fatal("a one-byte budget returned a page")
	}
	if !errors.Is(err, eventlog.ErrPageSize) {
		t.Fatal("the budget failure was not reported as a page-size error", err)
	}
	var budget *eventlog.PageBudgetError
	if !errors.As(err, &budget) {
		t.Fatal("the failure did not describe the record that did not fit", err)
	}
	if budget.Budget != 1 || budget.Required <= 1 {
		t.Fatal(budget)
	}
	if !strings.Contains(budget.Error(), "budget 1") {
		t.Fatal(budget.Error())
	}
}
