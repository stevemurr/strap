package inspection_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
)

func TestToolQueriesPinStateAndMatchArchiveAndHTTP(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	store, err := eventlog.NewJSONL(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(ctx)
	_, err = store.Append(ctx, eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)})
	if err != nil {
		t.Fatal(err)
	}
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
	appendEvent(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "agent", State: agent.Idle, StateRevision: 1}})
	now := time.Now()
	a := agent.ToolActivity{InvocationID: "agent/tool-1", Call: provider.ToolCall{ID: "reused", Name: "shell", Arguments: json.RawMessage(`{"input":{}}`)}, StartedAt: now}
	start := appendEvent(conversation.ToolEvent{Agent: "agent", Activity: a})
	reader, err := inspection.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	pinned, err := reader.At(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	a.FinishedAt = now.Add(time.Second)
	appendEvent(conversation.ToolEvent{Agent: "agent", Activity: a})
	a.InvocationID = "agent/tool-2"
	a.Call.Name = "read_file"
	a.StartedAt = now.Add(2 * time.Second)
	a.FinishedAt = time.Time{}
	appendEvent(conversation.ToolEvent{Agent: "agent", Activity: a})
	a.FinishedAt = now.Add(3 * time.Second)
	appendEvent(conversation.ToolEvent{Agent: "agent", Activity: a})
	if err = store.Seal(ctx, eventlog.Outcome{Reason: "requested"}); err != nil {
		t.Fatal(err)
	}
	old, err := pinned.InspectTool(ctx, "agent/tool-1")
	if err != nil || old.FinishedAt != nil {
		t.Fatal(old, err)
	}
	live, err := reader.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	all, err := live.ListTools(ctx, inspection.ToolQuery{})
	if err != nil || len(all.Items) != 2 || !all.End {
		t.Fatal(all, err)
	}
	first, err := live.ListTools(ctx, inspection.ToolQuery{PageQuery: inspection.PageQuery{Limit: 1}})
	if err != nil || first.End || len(first.Items) != 1 {
		t.Fatal(first, err)
	}
	second, err := live.ListTools(ctx, inspection.ToolQuery{PageQuery: inspection.PageQuery{After: first.Next, Limit: 1}})
	if err != nil || !second.End || len(second.Items) != 1 || second.Items[0].InvocationID != "agent/tool-2" {
		t.Fatal(second, err)
	}
	empty, err := live.ListTools(ctx, inspection.ToolQuery{Name: "missing"})
	if err != nil || !empty.End || empty.Next != live.Through().Sequence || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
	*all.Items[0].FinishedAt = time.Time{}
	again, err := live.InspectTool(ctx, "agent/tool-1")
	if err != nil || again.FinishedAt.IsZero() {
		t.Fatal("query result aliases state")
	}
	archive, err := inspection.OpenJSONL(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(ctx)
	av, err := archive.At(ctx, live.Through())
	if err != nil {
		t.Fatal(err)
	}
	archived, err := av.ListTools(ctx, inspection.ToolQuery{})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := live.ListTools(ctx, inspection.ToolQuery{})
	if err != nil || !reflect.DeepEqual(archived, direct) {
		t.Fatal("archive differs", err)
	}
	w := httptest.NewRecorder()
	inspection.Handler(archive).ServeHTTP(w, httptest.NewRequest("GET", "/tools", nil))
	var remote inspection.ToolPage
	if err = json.Unmarshal(w.Body.Bytes(), &remote); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	// JSON strips monotonic time metadata; compare their serialized contracts.
	want, _ := json.Marshal(direct)
	got, _ := json.Marshal(remote)
	if string(want) != string(got) {
		t.Fatal(string(want), string(got))
	}
	w = httptest.NewRecorder()
	inspection.Handler(archive).ServeHTTP(w, httptest.NewRequest("GET", "/tools?through=999", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
