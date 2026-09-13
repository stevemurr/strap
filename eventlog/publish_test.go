package eventlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
)

func note(payload string) eventlog.Data {
	return eventlog.Data{Kind: "note", Payload: json.RawMessage(payload)}
}

func newLog(t *testing.T, limits eventlog.Limits) *eventlog.Log {
	t.Helper()
	store, err := eventlog.NewMemory("session", eventlog.Limits{Entries: 1024, Bytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.New(store, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = log.Finish(context.Background(), eventlog.Outcome{Reason: "requested"})
		_ = log.Dispose(context.Background())
	})
	return log
}

// A record the log cannot store is refused by size rather than truncated.
func TestPublishRejectsOversizedAndInvalidRecords(t *testing.T) {
	log := newLog(t, eventlog.Limits{Entries: 8, Bytes: 8192})
	if err := log.Publish(note(`"` + strings.Repeat("x", 9000) + `"`)); !errors.Is(err, eventlog.ErrRecordSize) {
		t.Fatal("accepted a record larger than the log's byte limit", err)
	}
	// Invalid data is a capture failure: the log can no longer be trusted.
	if err := log.Publish(eventlog.Data{Kind: "", Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted a record with no kind")
	}
	if status := log.Status(); status.CaptureError == "" {
		t.Fatal("an invalid record did not mark the log as failed", status)
	}
}

// Publishing under a cancelled context never enqueues.
func TestPublishHonorsCallerCancellation(t *testing.T) {
	log := newLog(t, eventlog.Limits{Entries: 8, Bytes: 8192})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := log.PublishContext(ctx, note(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Once a log is finishing, further records are refused rather than racing the
// terminal record.
func TestPublishAfterFinishIsSealed(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.NewMemory("session", eventlog.Limits{Entries: 1024, Bytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.New(store, eventlog.Limits{Entries: 8, Bytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Publish(note(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := log.Finish(ctx, eventlog.Outcome{Reason: "requested"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Publish(note(`{}`)); !errors.Is(err, eventlog.ErrSealed) {
		t.Fatal("published after the log was sealed", err)
	}
	// Finishing again with the same outcome is idempotent; a different one is not.
	if err := log.Finish(ctx, eventlog.Outcome{Reason: "requested"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Finish(ctx, eventlog.Outcome{Reason: "something else"}); err == nil {
		t.Fatal("accepted a conflicting shutdown outcome")
	}
	if status := log.Status(); !status.Sealed {
		t.Fatal(status)
	}
	if err := log.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	if status := log.Status(); !status.Disposed {
		t.Fatal(status)
	}
	// Reads after disposal say so rather than returning an empty history.
	if _, err := log.Read(ctx, eventlog.Query{Limit: 10, MaxBytes: 1 << 20}); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
	if _, err := log.Subscribe(ctx, eventlog.Cursor{}); !errors.Is(err, eventlog.ErrDisposed) {
		t.Fatal(err)
	}
}

// A subscription is bound to one session and cannot start past the log's head.
func TestSubscribeValidatesItsStartingCursor(t *testing.T) {
	ctx := context.Background()
	log := newLog(t, eventlog.Limits{Entries: 8, Bytes: 8192})
	if err := log.Publish(note(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Subscribe(ctx, eventlog.Cursor{Session: "other", Sequence: 1}); !errors.Is(err, eventlog.ErrSession) {
		t.Fatal(err)
	}
	if _, err := log.Subscribe(ctx, eventlog.Cursor{Session: "session", Sequence: 9999}); !errors.Is(err, eventlog.ErrFuture) {
		t.Fatal(err)
	}
	sub, err := log.Subscribe(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	e, err := sub.Next(ctx)
	if err != nil || e.Kind != "note" {
		t.Fatal(e, err)
	}
	// A reader that stops waiting leaves the log usable for everyone else.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sub.Next(stopped); err == nil {
		t.Fatal("a cancelled reader was served an event")
	}
	if err := log.Publish(note(`{"second":true}`)); err != nil {
		t.Fatal(err)
	}
}

// Flush waits for everything accepted so far to reach storage.
func TestFlushWaitsForAcceptedRecords(t *testing.T) {
	ctx := context.Background()
	log := newLog(t, eventlog.Limits{Entries: 64, Bytes: 1 << 16})
	for i := 0; i < 20; i++ {
		if err := log.Publish(note(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	head, err := log.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head.Cursor.Sequence != 20 {
		t.Fatal("flush returned before every record was stored", head)
	}
	// With nothing outstanding, flushing is immediate even under a dead context.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := log.Flush(cancelled); err != nil {
		t.Fatal("flushing an already-drained log did work", err)
	}
	// Wait resolves once the log advances past a cursor.
	if _, err := log.Wait(ctx, eventlog.Cursor{Session: "session", Sequence: 19}); err != nil {
		t.Fatal(err)
	}
	// A waiter parked at the head resolves when the next record lands.
	waited := make(chan eventlog.Head, 1)
	go func() {
		h, err := log.Wait(ctx, eventlog.Cursor{Session: "session", Sequence: 20})
		if err != nil {
			t.Error(err)
		}
		waited <- h
	}()
	if err := log.Publish(note(`{"next":true}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-waited:
		if h.Cursor.Sequence < 21 {
			t.Fatal("Wait resolved before the log advanced", h)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait never resolved after the log advanced")
	}
}

// A failed log reports its capture error to every caller rather than accepting
// records it can never store.
func TestFailedLogRefusesFurtherWork(t *testing.T) {
	ctx := context.Background()
	log := newLog(t, eventlog.Limits{Entries: 8, Bytes: 8192})
	log.Fail(errors.New("disk gone"))
	if err := log.Publish(note(`{}`)); err == nil {
		t.Fatal("published to a failed log")
	}
	if status := log.Status(); !strings.Contains(status.CaptureError, "disk gone") {
		t.Fatal(status)
	}
	if err := log.Flush(ctx); err == nil {
		t.Fatal("flushed a failed log")
	}
	// A second failure does not replace the first, which is the real cause.
	log.Fail(errors.New("and then the power"))
	if status := log.Status(); !strings.Contains(status.CaptureError, "disk gone") {
		t.Fatal("a later failure displaced the original cause", status)
	}
}
