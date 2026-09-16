package httpapi_test

import (
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
	"reflect"
	"testing"
)

func TestExplicitCreationAndAssignmentWireContracts(t *testing.T) {
	ctx, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()
	for _, raw := range []string{`{}`, `{"role":null}`, `{"role":""}`, `{"role":"root"}`, `{"role":"implementor","task":"hidden work"}`, `{"role":"implementor","profile":"legacy"}`} {
		response := request(t, s.http, "POST", base+"/agents", httpapi.WorkRequest[json.RawMessage]{Actor: s.Root(), Request: json.RawMessage(raw)})
		if response.Code != 400 || len(s.Agents()) != 1 {
			t.Fatal(raw, response.Code, response.Body.String())
		}
	}
	legacy := request(t, s.http, "POST", base+"/agents", map[string]any{"parent": s.Root(), "profile": "implementor"})
	if legacy.Code != 400 {
		t.Fatal(legacy.Code)
	}
	impl := createHTTPWorker(t, s)
	for _, raw := range []string{`{"kind":"implementation","task":"task"}`, `{"kind":"implementation","assignee":null,"task":"task"}`, fmt.Sprintf(`{"kind":"implementation","assignee":%q,"task":"task","work_id":""}`, impl), fmt.Sprintf(`{"kind":"audit","assignee":%q,"work_id":"w","expected_revision":1,"submission_id":"s","task":""}`, impl)} {
		response := request(t, s.http, "POST", base+"/work/assign", httpapi.WorkRequest[json.RawMessage]{Actor: s.Root(), Request: json.RawMessage(raw)})
		if response.Code != 400 {
			t.Fatal(raw, response.Code, response.Body.String())
		}
	}
	page, e := s.ListWork(ctx, s.Root(), work.ListQuery{})
	if e != nil || len(page.Items) != 0 || len(s.Agents()) != 2 {
		t.Fatal(page, e)
	}
	for _, raw := range []string{`{"work_id":"w","expected_revision":1}`, `{"work_id":"w","expected_revision":1,"assignee":null}`, `{"work_id":"w","expected_revision":1,"assignee":""}`} {
		response := request(t, s.http, "POST", base+"/work/reassign", httpapi.WorkRequest[json.RawMessage]{Actor: s.Root(), Request: json.RawMessage(raw)})
		if response.Code != 400 {
			t.Fatal(raw, response.Code)
		}
	}
	role := request(t, s.http, "GET", base+"/agents/"+string(impl), nil)
	var info harness.AgentInspection
	if e = json.Unmarshal(role.Body.Bytes(), &info); e != nil || info.Role != roster.Implementor || !info.Registered {
		t.Fatal(info, e)
	}
	unauthorized := request(t, s.http, "POST", base+"/agents", httpapi.WorkRequest[roster.CreateRequest]{Actor: impl, Request: roster.CreateRequest{Role: roster.Auditor}})
	if unauthorized.Code != 403 {
		t.Fatal(unauthorized.Code)
	}
	for i := 0; i < 3; i++ {
		if _, e = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: impl, Task: "task"}); e != nil {
			t.Fatal(e)
		}
	}
	listed := request(t, s.http, "GET", base+"/work?actor="+string(s.Root())+"&limit=1", nil)
	var first work.ListPage
	if e = json.Unmarshal(listed.Body.Bytes(), &first); e != nil || listed.Code != 200 || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(listed.Code, first, e)
	}
	next := request(t, s.http, "GET", base+"/work?actor="+string(s.Root())+"&cursor="+first.NextCursor, nil)
	var got work.ListPage
	if e = json.Unmarshal(next.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	want, e := s.ListWork(ctx, s.Root(), work.ListQuery{Cursor: first.NextCursor})
	if e != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, want, e)
	}
	archived := request(t, s.http, "GET", base+"/trace/work?actor="+string(s.Root())+"&cursor="+first.NextCursor, nil)
	if archived.Code != 200 || archived.Body.String() != next.Body.String() {
		t.Fatal(archived.Code, archived.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=101", "state=invalid", "kind=invalid", "limit=1&limit=2", "unknown=1", "cursor=" + first.NextCursor + "&state="} {
		response := request(t, s.http, "GET", base+"/work?actor="+string(s.Root())+"&"+query, nil)
		if response.Code != 400 {
			t.Fatal(query, response.Code)
		}
	}
	denied := request(t, s.http, "GET", base+"/work?actor="+string(impl), nil)
	if denied.Code != 403 {
		t.Fatal(denied.Code)
	}
}
