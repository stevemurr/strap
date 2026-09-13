package inspection_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

func TestViewsPinLiveAndInterruptedArchivePrefixes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	store, err := eventlog.NewJSONL(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	if _, err = store.Append(ctx, eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)}); err != nil {
		t.Fatal(err)
	}
	id := identity.OutputID{Agent: "agent", Call: 1}
	appendEvent := func(e conversation.Event) eventlog.Cursor {
		t.Helper()
		d, err := eventcodec.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		r, err := store.Append(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		return r.Cursor()
	}
	appendEvent(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "agent", Parent: "user", State: agent.Idle, StateRevision: 1}})
	appendEvent(conversation.AgentEvent{Agent: "agent", Event: agent.HistoryAppended{Position: 1, Message: provider.Message{Role: "system"}}})
	appendEvent(conversation.AgentEvent{Agent: "agent", Event: agent.OutputStarted{Output: id, ContextRevision: 1, StartedAt: time.Now()}})
	cursor := appendEvent(conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: id, Channel: provider.ChannelContent, Text: "hé"}})
	reader, err := inspection.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	view, err := reader.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: id, Channel: provider.ChannelContent, Offset: 3, Text: "llo"}})
	old, err := reader.At(ctx, cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []*inspection.View{view, old} {
		page, err := v.ReadOutputText(ctx, inspection.OutputTextQuery{Output: id, MaxBytes: 32})
		if err != nil || page.Text != "hé" || !page.End || page.Through != cursor {
			t.Fatalf("pinned text: %+v %v", page, err)
		}
		if _, err = v.ReadRecord(ctx, eventlog.Cursor{Session: "session", Sequence: cursor.Sequence + 1}); !errors.Is(err, eventlog.ErrFuture) {
			t.Fatal(err)
		}
	}
	if err = store.Close(ctx); err != nil {
		t.Fatal(err)
	}
	archive, err := inspection.OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := archive.Head(ctx)
	if err != nil || head.State != eventlog.Failed {
		t.Fatalf("interrupted head: %+v %v", head, err)
	}
	av, err := archive.At(ctx, cursor)
	if err != nil {
		t.Fatal(err)
	}
	page, err := av.ReadOutputText(ctx, inspection.OutputTextQuery{Output: id, MaxBytes: 32})
	if err != nil || page.Text != "hé" {
		t.Fatal(page, err)
	}
	if err = archive.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = av.ReadRecord(ctx, cursor); !errors.Is(err, inspection.ErrClosed) {
		t.Fatal(err)
	}
}

func TestClosingBorrowedReaderLeavesStoreUsable(t *testing.T) {
	ctx := context.Background()
	s, err := eventlog.NewMemory("session", eventlog.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	r, err := inspection.New(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(ctx, eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.At(ctx, eventlog.Cursor{}); !errors.Is(err, inspection.ErrClosed) {
		t.Fatal(err)
	}
}

func TestReaderCloseCancelsAndJoinsActiveSourceReads(t *testing.T) {
	ctx := context.Background()
	store, err := eventlog.NewMemory("session", eventlog.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	_, err = store.Append(ctx, eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)})
	if err != nil {
		t.Fatal(err)
	}
	source := &blockingSource{Source: store, entered: make(chan struct{}), exited: make(chan struct{})}
	r, err := inspection.New(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := r.At(ctx, eventlog.Cursor{}); result <- err }()
	<-source.entered
	closeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = r.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.exited:
	default:
		t.Fatal("close returned before read exited")
	}
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type blockingSource struct {
	inspection.Source
	entered, exited chan struct{}
}

func (s *blockingSource) Read(ctx context.Context, q eventlog.Query) (eventlog.Page, error) {
	close(s.entered)
	<-ctx.Done()
	close(s.exited)
	return eventlog.Page{}, ctx.Err()
}
