package httpapi_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func TestExplicitCreationAndAssignmentWireContracts(t *testing.T) {
	ctx, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()
	for _, raw := range []string{`{}`, `{"role":null}`, `{"role":""}`, `{"role":"manager"}`, `{"role":"implementor","task":"hidden work"}`, `{"role":"implementor","profile":"legacy"}`} {
		response := request(t, s.http, "POST", base+"/agents", wireRequest(s.Manager(), json.RawMessage(raw)))
		// A session starts with its manager alone.
		if response.Code != 400 || len(s.Agents()) != 1 {
			t.Fatal(raw, response.Code, response.Body.String())
		}
	}
	legacy := request(t, s.http, "POST", base+"/agents", map[string]any{"parent": s.Manager(), "profile": "implementor"})
	if legacy.Code != 400 {
		t.Fatal(legacy.Code)
	}
	impl := createHTTPWorker(t, s)
	for _, tc := range []struct {
		action string
		raw    string
	}{
		{"assign_task", `{"task":"task"}`},
		{"assign_task", `{"assignee":null,"task":"task","context":null,"expected_output":null,"scope":null}`},
		{"assign_task", fmt.Sprintf(`{"assignee":%q,"task":"task","work_id":""}`, impl)},
		{"assign_task", fmt.Sprintf(`{"assignee":%q,"task":"task","kind":"implementation"}`, impl)},
		{"assign_task", fmt.Sprintf(`{"assignee":%q,"task":"task","context":null}`, impl)},
		{"assign_audit", fmt.Sprintf(`{"assignee":%q,"work_id":"w","expected_revision":1,"submission_id":"s","task":""}`, impl)},
		{"assign_audit", fmt.Sprintf(`{"assignee":%q,"work_id":"w","expected_revision":1,"audit_id":"a"}`, impl)},
		{"assign_repair", fmt.Sprintf(`{"assignee":%q,"work_id":"w","expected_revision":1,"submission_id":"s"}`, impl)},
		{"assign_task", fmt.Sprintf(`{"assignee":%q,"task":"task","scope":{"plan_id":"p","step_ids":["s"]}}`, impl)},
	} {
		response := request(t, s.http, "POST", base+"/work/"+tc.action, wireRequest(s.Manager(), json.RawMessage(tc.raw)))
		if response.Code != 400 {
			t.Fatal(tc.action, tc.raw, response.Code, response.Body.String())
		}
	}
	removed := request(t, s.http, "POST", base+"/work/assign", wireRequest(s.Manager(), json.RawMessage(fmt.Sprintf(`{"kind":"implementation","assignee":%q,"task":"task"}`, impl))))
	if removed.Code != 404 {
		t.Fatal("removed assignment route remains available", removed.Code, removed.Body.String())
	}
	page, e := s.ListWork(ctx, s.Manager(), work.ListQuery{})
	if e != nil || len(page.Items) != 0 || len(s.Agents()) != 2 {
		t.Fatal(page, e)
	}
	if response := request(t, s.http, "POST", base+"/work/reassign", wireRequest(s.Manager(), json.RawMessage(`{"work_id":"w","expected_revision":1,"assignee":"agent-3"}`))); response.Code != 404 && response.Code != 400 {
		t.Fatal("removed reassign route remains available", response.Code)
	}
	role := request(t, s.http, "GET", base+"/agents/"+string(impl), nil)
	var info harness.AgentInspection
	if e = json.Unmarshal(role.Body.Bytes(), &info); e != nil || info.Role != roster.Implementor || !info.Registered {
		t.Fatal(info, e)
	}
	unauthorized := request(t, s.http, "POST", base+"/agents", wireRequest(impl, roster.CreateRequest{Role: roster.Auditor}))
	if unauthorized.Code != 403 {
		t.Fatal(unauthorized.Code)
	}
	for i := 0; i < 3; i++ {
		if _, e = s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: impl, Task: "task"}); e != nil {
			t.Fatal(e)
		}
	}
	listed := request(t, s.http, "GET", base+"/work?actor="+string(s.Manager())+"&limit=1", nil)
	var first work.ListPage
	if e = json.Unmarshal(listed.Body.Bytes(), &first); e != nil || listed.Code != 200 || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(listed.Code, first, e)
	}
	next := request(t, s.http, "GET", base+"/work?actor="+string(s.Manager())+"&cursor="+first.NextCursor, nil)
	var got work.ListPage
	if e = json.Unmarshal(next.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	want, e := s.ListWork(ctx, s.Manager(), work.ListQuery{Cursor: first.NextCursor})
	if e != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, want, e)
	}
	archived := request(t, s.http, "GET", base+"/trace/work?actor="+string(s.Manager())+"&cursor="+first.NextCursor, nil)
	if archived.Code != 200 || archived.Body.String() != next.Body.String() {
		t.Fatal(archived.Code, archived.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=101", "state=invalid", "kind=invalid", "limit=1&limit=2", "unknown=1", "cursor=" + first.NextCursor + "&state="} {
		response := request(t, s.http, "GET", base+"/work?actor="+string(s.Manager())+"&"+query, nil)
		if response.Code != 400 {
			t.Fatal(query, response.Code)
		}
	}
	denied := request(t, s.http, "GET", base+"/work?actor="+string(impl), nil)
	if denied.Code != 403 {
		t.Fatal(denied.Code)
	}
}

func TestResearchAssignmentUsesItsOperationContract(t *testing.T) {
	ctx, s := recoverySession(t, true)
	researcher, err := s.CreateAgent(ctx, s.Manager(), roster.CreateRequest{Role: roster.WebResearcher})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, s.http, "POST", "/sessions/"+s.ID()+"/work/assign_task", wireRequest(s.Manager(), tool.AssignTaskArgs{Kind: work.WebResearch, Assignee: ptr(researcher.AgentID), Task: "Investigate the protocol", Context: testString("Review its documented limits"), ExpectedOutput: testString("A concise set of findings")}))
	var assigned work.Work
	if err := json.Unmarshal(response.Body.Bytes(), &assigned); err != nil || response.Code != 200 {
		t.Fatal(response.Code, response.Body.String(), err)
	}
	if assigned.Kind != work.WebResearch || assigned.Assignee != researcher.AgentID || assigned.Task != "Investigate the protocol" {
		t.Fatal(assigned)
	}
}

func testString(s string) *string { return &s }

func TestRemovedProgressMutationAndFlatCommandsAreRejected(t *testing.T) {
	ctx, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()
	worker := createHTTPWorker(t, s)
	before, err := s.ListWork(ctx, s.Manager(), work.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"assign_task", "cancel", "submit", "brief", "audit", "report-progress"} {
		flat := map[string]any{"actor": s.Manager(), "request": tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(worker), Task: "must not run"}}
		response := request(t, s.http, "POST", base+"/work/"+action, flat)
		if response.Code != 400 {
			t.Fatalf("%s: %d %s", action, response.Code, response.Body.String())
		}
	}
	response := request(t, s.http, "POST", base+"/work/progress", wireRequest(s.Manager(), map[string]any{"work_id": "w"}))
	if response.Code != 404 {
		t.Fatalf("removed route: %d %s", response.Code, response.Body.String())
	}
	after, err := s.ListWork(ctx, s.Manager(), work.ListQuery{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected commands changed work", err)
	}
}
