package inspection_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// traceScript answers with one tool call and then a plain message so the trace
// carries tool activity, outputs and history rather than a single bare turn.
type traceScript struct{ step atomic.Int32 }

func (p *traceScript) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	if p.step.Add(1) == 1 {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "list_agents", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	return provider.Response{Content: "done"}, nil
}

type traceFixture struct {
	ctx     context.Context
	reader  *inspection.Reader
	handler http.Handler
	root    identity.ActorID
	worker  identity.ActorID
	through uint64
}

// tracedSession runs a short session to completion and serves its sealed trace.
func tracedSession(t *testing.T) traceFixture {
	t.Helper()
	ctx := context.Background()
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LocalTools = t.TempDir(), nil, false
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: &traceScript{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Dispose(ctx) })

	reg, err := s.CreateAgent(ctx, s.Root(), roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: reg.AgentID, Task: "inspect me"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Send(s.Root(), "hello"); err != nil {
		t.Fatal(err)
	}
	// The root returns to Idle once its tool call and reply have been recorded.
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(wait)
		if err != nil {
			t.Fatal("session never settled", err)
		}
		if c, ok := e.(conversation.AgentStateChanged); ok && c.Agent == s.Root() && c.State == agent.Idle {
			break
		}
	}
	reader, err := s.Trace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(ctx) })
	head, err := reader.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return traceFixture{ctx: ctx, reader: reader, handler: inspection.Handler(reader), root: s.Root(), worker: reg.AgentID, through: head.Cursor.Sequence}
}

func (f traceFixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

// decode asserts a 200 and unmarshals the body into T.
func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Every read route resolves against the same pinned prefix and returns the
// recorded shape rather than a live re-derivation.
func TestInspectionHandlerServesEveryReadRoute(t *testing.T) {
	f := tracedSession(t)

	session := decode[inspection.SessionView](t, f.get(t, "/"))
	if session.Through.Sequence == 0 || len(session.Configuration) == 0 {
		t.Fatal("session view lost its pinned prefix or configuration", session)
	}
	if named := decode[inspection.SessionView](t, f.get(t, "/session")); named.ID != session.ID {
		t.Fatal("/ and /session disagree", named, session)
	}

	agents := decode[inspection.AgentPage](t, f.get(t, "/agents"))
	if len(agents.Items) < 2 {
		t.Fatal("trace did not record both the root and the worker", agents)
	}
	if one := decode[any](t, f.get(t, "/agents/"+string(f.worker))); one == nil {
		t.Fatal("worker inspection was empty")
	}

	tools := decode[inspection.ToolPage](t, f.get(t, "/tools"))
	if len(tools.Items) == 0 {
		t.Fatal("trace did not record the scripted tool call")
	}
	call := tools.Items[0]
	if got := decode[inspection.ToolView](t, f.get(t, "/tools/"+string(call.InvocationID))); got.InvocationID != call.InvocationID {
		t.Fatal("tool inspection returned a different invocation", got)
	}
	match := fmt.Sprintf("/tools?agent=%s&name=%s", call.Agent, call.Name)
	if filtered := decode[inspection.ToolPage](t, f.get(t, match)); len(filtered.Items) == 0 {
		t.Fatal("agent and name filters excluded the recorded call", match)
	}
	if other := decode[inspection.ToolPage](t, f.get(t, "/tools?name=no_such_tool")); len(other.Items) != 0 {
		t.Fatal("name filter matched an unrecorded tool", other)
	}

	outputs := decode[inspection.OutputPage](t, f.get(t, "/outputs"))
	if len(outputs.Items) == 0 {
		t.Fatal("trace did not record any model output")
	}
	first := outputs.Items[0]
	path := fmt.Sprintf("/outputs/%s/%d", first.ID.Agent, first.ID.Call)
	if got := decode[inspection.OutputView](t, f.get(t, path)); got.ID != first.ID {
		t.Fatal("output inspection returned a different output", got)
	}
	if text := decode[inspection.TextPage](t, f.get(t, path+"/text")); text.Through.Sequence == 0 {
		t.Fatal("text page was not pinned to the prefix", text)
	}
	if byAgent := decode[inspection.OutputPage](t, f.get(t, "/outputs?agent="+string(f.root))); len(byAgent.Items) == 0 {
		t.Fatal("agent filter excluded the root's outputs")
	}

	if rec := decode[map[string]any](t, f.get(t, "/records/1")); rec["sequence"] == nil {
		t.Fatal("record read returned no sequence", rec)
	}

	listed := decode[work.ListPage](t, f.get(t, "/work?actor="+string(f.root)))
	if len(listed.Items) != 1 {
		t.Fatal("work listing did not return the assignment", listed)
	}
}

// Pagination is explicit: a limit stops the page early and the returned cursor
// resumes exactly where it left off.
func TestInspectionHandlerPaginatesWithinThePinnedPrefix(t *testing.T) {
	f := tracedSession(t)
	page := decode[inspection.AgentPage](t, f.get(t, "/agents?limit=1"))
	if len(page.Items) != 1 || page.End {
		t.Fatal("limit=1 did not stop the page early", page)
	}
	rest := decode[inspection.AgentPage](t, f.get(t, fmt.Sprintf("/agents?after=%d", page.Next)))
	if len(rest.Items) == 0 {
		t.Fatal("cursor did not resume the listing", rest)
	}
	for _, a := range rest.Items {
		if a.ID == page.Items[0].ID {
			t.Fatal("cursor replayed an item from the previous page", a.ID)
		}
	}
	pinned := decode[inspection.AgentPage](t, f.get(t, fmt.Sprintf("/agents?through=%d", f.through)))
	if pinned.Through.Sequence != f.through {
		t.Fatal("through was not honored", pinned.Through)
	}
}

// Malformed reads are rejected before any source read, each with the status
// that tells the caller whether to fix the query or give up.
func TestInspectionHandlerRejectsMalformedReads(t *testing.T) {
	f := tracedSession(t)
	out := fmt.Sprintf("/outputs/%s/1", f.root)
	cases := []struct {
		path   string
		status int
	}{
		{"/agents?through=nope", 400},
		{"/agents?after=nope", 400},
		{"/agents?limit=nope", 400},
		{"/agents?limit=0", 400},
		{"/agents?limit=1001", 400},
		{"/agents?session=other", 400},
		{fmt.Sprintf("/agents?through=%d", f.through+5000), 400},
		{"/outputs/agent/zero", 400},
		{"/outputs/agent/0", 400},
		{out + "/text?offset=nope", 400},
		{out + "/text?max_bytes=0", 400},
		{out + "/text?max_bytes=2097152", 400},
		{"/records/nope", 400},
		{"/work?unknown=1", 400},
		{"/work?limit=0", 400},
		{"/work?limit=nope", 400},
		{"/work?cursor=x&state=active", 400},
		{"/agents/missing", 404},
		{"/tools/missing", 404},
		{"/outputs/missing/1", 404},
		{"/nowhere", 404},
		{"/agents/a/b/c", 404},
		{"/records", 404},
	}
	for _, tc := range cases {
		if w := f.get(t, tc.path); w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	// A duplicated work filter is ambiguous rather than last-wins.
	if w := f.get(t, "/work?state=active&state=done"); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Inspection is read-only.
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("POST", "/agents", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatal(w.Code, w.Header())
	}
}
