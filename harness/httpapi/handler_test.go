package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/roster"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type idle struct{}

func (idle) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: "ready"}, nil
}
func config() harness.Config {
	c := harness.DefaultConfig()
	c.Web = nil
	c.LocalTools = false
	c.Telemetry.ContextTokens = false
	return c
}
func request(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type testSession struct {
	*harness.Session
	http http.Handler
}

func recoverySession(t *testing.T, viaHTTP bool) (context.Context, *testSession) {
	t.Helper()
	ctx := context.Background()
	var session *harness.Session
	if !viaHTTP {
		s, err := harness.New(ctx, config(), harness.Dependencies{Provider: idle{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Dispose(ctx); err != nil {
				t.Error(err)
			}
		})
		return ctx, &testSession{Session: s}
	}
	service, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: config(), Authorize: httpapi.BearerToken("test-token"), Factory: func(ctx context.Context, c harness.Config) (*harness.Session, error) {
		var err error
		session, err = harness.New(ctx, c, harness.Dependencies{Provider: idle{}})
		return session, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, service, "POST", "/sessions", httpapi.CreateRequest{})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return ctx, &testSession{Session: session, http: service}
}
func operationResult[T any](t *testing.T, s *testSession, viaHTTP bool, actor identity.ActorID, name string, params any, direct func() (T, error)) T {
	t.Helper()
	var value T
	if !viaHTTP {
		v, err := direct()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	action := map[string]string{"assign_implementation": "assign_implementation", "assign_audit": "assign_audit", "assign_repair": "assign_repair", "assign_research": "assign_research", "reassign_work": "reassign", "cancel_work": "cancel", "create_plan": "plan", "submit_work": "submit", "submit_audit": "audit", "report_work_progress": "report-progress"}[name]
	method, path, body := "POST", "/sessions/"+s.ID()+"/work/"+action, any(httpapi.WorkRequest[any]{Actor: actor, Request: tool.Input[any]{Value: params}})
	if name == "create_plan" {
		body = httpapi.WorkRequest[any]{Actor: actor, Request: params}
	}
	if name == "create_agent" {
		path = "/sessions/" + s.ID() + "/agents"
	}
	if m, ok := params.(map[string]any); ok {
		method = "GET"
		body = nil
		switch name {
		case "get_work":
			path = "/sessions/" + s.ID() + "/work/" + fmt.Sprint(m["work_id"])
		case "get_plan":
			path = "/sessions/" + s.ID() + "/plans/" + fmt.Sprint(m["plan_id"])
		case "get_audit":
			path = "/sessions/" + s.ID() + "/audits/" + fmt.Sprint(m["audit_id"])
		}
		path += "?actor=" + string(actor)
	}
	w := request(t, s.http, method, path, body)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func ptr[T any](v T) *T { return &v }

type operationOutcome struct {
	Plan      work.Plan
	Work      work.Inspection
	Audit     work.Audit
	Cancelled work.Work
}

func operationCycle(t *testing.T, viaHTTP bool) operationOutcome {
	t.Helper()
	ctx, s := recoverySession(t, viaHTTP)
	root := s.Root()
	create := func(role roster.Role) roster.Registration {
		r := roster.CreateRequest{Role: role}
		return operationResult(t, s, viaHTTP, root, "create_agent", r, func() (roster.Registration, error) { return s.CreateAgent(ctx, root, r) })
	}
	implementor := create(roster.Implementor)
	auditor := create(roster.Auditor)
	planRequest := work.PlanUpdate{Title: ptr("Storage"), Steps: []work.StepEdit{{Title: ptr("Implement")}, {Title: ptr("Unassigned")}}}
	plan := operationResult(t, s, viaHTTP, root, "create_plan", planRequest, func() (work.Plan, error) {
		return s.UpdatePlan(ctx, root, planRequest)
	})
	assignment := work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "implement storage", Scope: &work.Scope{PlanID: plan.ID, StepIDs: []work.StepID{plan.Steps[0].ID}}}
	implementation := operationResult(t, s, viaHTTP, root, "assign_implementation", tool.AssignImplementationArgs{Assignee: assignment.Assignee, Task: assignment.Task, Context: &assignment.Context, ExpectedOutput: &assignment.ExpectedOutput, Scope: assignment.Scope}, func() (work.Work, error) {
		return s.AssignWork(ctx, root, assignment)
	})
	implementationID := implementation.ID
	var lastAudit work.Audit
	for _, verdict := range []work.Verdict{work.Fail, work.Pass} {
		progress := work.ReportWorkProgressRequest{WorkID: implementation.ID, Steps: []work.StepProgress{{ID: plan.Steps[0].ID, Status: ptr(work.ReadyForReview)}}}
		receipt := operationResult(t, s, viaHTTP, implementation.Assignee, "report_work_progress", tool.ReportWorkProgressInput{WorkID: progress.WorkID, Steps: []tool.StepProgressInput{{ID: plan.Steps[0].ID, Status: ptr(work.ReadyForReview)}}}, func() (work.ReportWorkProgressResult, error) {
			return s.ReportWorkProgress(ctx, implementation.Assignee, progress)
		})
		implementation.Revision = receipt.WorkRevision
		submissionRequest := work.SubmitRequest{WorkTarget: work.WorkTarget{ID: implementation.ID, ExpectedRevision: implementation.Revision}, Summary: "implemented", Evidence: []string{"checked"}}
		submission := operationResult(t, s, viaHTTP, implementation.Assignee, "submit_work", tool.SubmitInput{WorkTarget: submissionRequest.WorkTarget, Summary: submissionRequest.Summary, Evidence: submissionRequest.Evidence}, func() (work.SubmitReceipt, error) {
			return s.SubmitWork(ctx, implementation.Assignee, submissionRequest)
		})
		originalView := operationResult(t, s, viaHTTP, root, "get_work", map[string]any{"work_id": implementationID}, func() (work.Inspection, error) { return s.InspectWork(ctx, root, implementationID) })
		original := originalView.Work
		auditRequest := work.AssignmentRequest{Kind: work.AuditWork, Assignee: auditor.AgentID, WorkID: original.ID, ExpectedRevision: original.Revision, SubmissionID: submission.ID}
		auditing := operationResult(t, s, viaHTTP, root, "assign_audit", tool.AssignAuditArgs{Assignee: auditRequest.Assignee, WorkTarget: work.WorkTarget{ID: auditRequest.WorkID, ExpectedRevision: auditRequest.ExpectedRevision}, SubmissionID: auditRequest.SubmissionID}, func() (work.Work, error) {
			return s.AssignWork(ctx, root, auditRequest)
		})
		inspection := operationResult(t, s, viaHTTP, auditing.Assignee, "get_work", map[string]any{"work_id": auditing.ID}, func() (work.Inspection, error) {
			return s.InspectWork(ctx, auditing.Assignee, auditing.ID)
		})
		if inspection.Submission == nil || inspection.Submission.ID != submission.ID || len(inspection.Steps) != 1 {
			t.Fatalf("missing scoped submission: %+v", inspection)
		}
		verdictRequest := work.AuditRequest{WorkTarget: work.WorkTarget{ID: auditing.ID, ExpectedRevision: auditing.Revision}, SubmissionID: submission.ID, Verdict: verdict, Summary: "reviewed"}
		if verdict == work.Fail {
			verdictRequest.Findings = []work.Finding{{StepIDs: []work.StepID{plan.Steps[0].ID}, Description: "missing check", RequiredChange: "handle error", Verification: "test failure"}}
		}
		verdictInput := tool.AuditInput{WorkTarget: verdictRequest.WorkTarget, SubmissionID: verdictRequest.SubmissionID, Verdict: verdictRequest.Verdict, Summary: verdictRequest.Summary}
		for _, f := range verdictRequest.Findings {
			verdictInput.Findings = append(verdictInput.Findings, tool.FindingInput{StepIDs: f.StepIDs, Description: f.Description, RequiredChange: f.RequiredChange, Verification: f.Verification})
		}
		lastAudit = operationResult(t, s, viaHTTP, auditing.Assignee, "submit_audit", verdictInput, func() (work.Audit, error) {
			return s.SubmitAudit(ctx, auditing.Assignee, verdictRequest)
		})
		if verdict == work.Fail {
			original, e := s.GetWork(ctx, root, implementationID)
			if e != nil {
				t.Fatal(e)
			}
			repairRequest := work.AssignmentRequest{Kind: work.Repair, Assignee: implementation.Assignee, WorkID: original.ID, ExpectedRevision: original.Revision, AuditID: lastAudit.ID}
			repairWork := operationResult(t, s, viaHTTP, root, "assign_repair", tool.AssignRepairArgs{Assignee: repairRequest.Assignee, WorkTarget: work.WorkTarget{ID: repairRequest.WorkID, ExpectedRevision: repairRequest.ExpectedRevision}, AuditID: repairRequest.AuditID}, func() (work.Work, error) { return s.AssignWork(ctx, root, repairRequest) })
			repair := operationResult(t, s, viaHTTP, implementation.Assignee, "get_work", map[string]any{"work_id": repairWork.ID}, func() (work.Inspection, error) {
				return s.InspectWork(ctx, implementation.Assignee, repairWork.ID)
			})
			if repair.Audit == nil || repair.Audit.ID != lastAudit.ID || repair.Work.Assignee != implementation.Assignee {
				t.Fatalf("repair did not preserve findings and assignee: %+v", repair)
			}
			implementation = repair.Work
		}
	}
	final := operationResult(t, s, viaHTTP, root, "get_work", map[string]any{"work_id": implementationID}, func() (work.Inspection, error) {
		return s.InspectWork(ctx, root, implementationID)
	})
	plan = operationResult(t, s, viaHTTP, root, "get_plan", map[string]any{"plan_id": plan.ID}, func() (work.Plan, error) {
		return s.GetPlan(ctx, root, plan.ID)
	})
	audit := operationResult(t, s, viaHTTP, root, "get_audit", map[string]any{"audit_id": lastAudit.ID}, func() (work.Audit, error) {
		return s.GetAudit(ctx, root, lastAudit.ID)
	})
	if final.Work.State != work.Accepted || plan.Steps[0].Status != work.Completed || plan.Steps[1].Status != work.Pending || audit.Verdict != work.Pass {
		t.Fatalf("unexpected audit/repair outcome: %+v, %+v, %+v", final, plan, audit)
	}
	assignment = work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "reassign then cancel"}
	extra := operationResult(t, s, viaHTTP, root, "assign_implementation", tool.AssignImplementationArgs{Assignee: assignment.Assignee, Task: assignment.Task, Context: &assignment.Context, ExpectedOutput: &assignment.ExpectedOutput, Scope: assignment.Scope}, func() (work.Work, error) {
		return s.AssignWork(ctx, root, assignment)
	})
	oldAssignee := extra.Assignee
	replacement := create(roster.Implementor)
	reassign := work.ReassignRequest{Assignee: replacement.AgentID, WorkTarget: work.WorkTarget{ID: extra.ID, ExpectedRevision: extra.Revision}}
	extra = operationResult(t, s, viaHTTP, root, "reassign_work", reassign, func() (work.Work, error) {
		return s.ReassignWork(ctx, root, reassign)
	})
	if extra.Assignee == oldAssignee {
		t.Fatal("replacement was not provisioned")
	}
	cancel := work.CancelRequest{WorkTarget: work.WorkTarget{ID: extra.ID, ExpectedRevision: extra.Revision}, Reason: "withdrawn"}
	extra = operationResult(t, s, viaHTTP, root, "cancel_work", cancel, func() (work.Work, error) {
		return s.CancelWork(ctx, root, cancel)
	})
	if extra.State != work.Cancelled {
		t.Fatal(extra)
	}
	return operationOutcome{Plan: plan, Work: final, Audit: audit, Cancelled: extra}
}

func TestHTTPAndDirectOperationsProduceSameAuditRepairOutcome(t *testing.T) {
	direct := operationCycle(t, false)
	adapted := operationCycle(t, true)
	if a, b := opaqueIDs(t, direct), opaqueIDs(t, adapted); a != b {
		t.Fatalf("typed operations and tool adapters diverged:\ndirect: %s\nadapted: %s", a, b)
	}
}

// opaqueIDs replaces every store-issued id with its position of first
// appearance, so two independent runs can be compared structurally even though
// each run issues its own random ids.
func opaqueIDs(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	return regexp.MustCompile(`\b([a-z]+)-[0-9a-z]{7}\b`).ReplaceAllStringFunc(string(raw), func(id string) string {
		if _, ok := seen[id]; !ok {
			seen[id] = fmt.Sprintf("%s#%d", id[:strings.Index(id, "-")], len(seen)+1)
		}
		return seen[id]
	})
}

func TestHTTPAuthorizationValidationAndConflictErrors(t *testing.T) {
	_, s := recoverySession(t, true)
	base := "/sessions/" + s.ID()
	unauth := httptest.NewRecorder()
	s.http.ServeHTTP(unauth, httptest.NewRequest("GET", base, nil))
	if unauth.Code != 403 {
		t.Fatal(unauth.Code)
	}
	cases := []struct {
		method, path string
		body         any
		status       int
	}{
		{"GET", base + "/events?after=999999", nil, 400},
		{"GET", base + "/events?limit=0", nil, 400},
		{"GET", base + "/agents/missing", nil, 404},
		{"POST", base + "/messages", map[string]any{"to": s.Root(), "unexpected": true}, 400},
		{"POST", base + "/work/assign_implementation", wireRequest("intruder", tool.AssignImplementationArgs{Assignee: "worker", Task: "forbidden"}), 403},
	}
	for _, tc := range cases {
		w := request(t, s.http, tc.method, tc.path, tc.body)
		if w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	w := request(t, s.http, "POST", base+"/work/assign_implementation", wireRequest(s.Root(), tool.AssignImplementationArgs{Assignee: createHTTPWorker(t, s), Task: "task"}))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var item work.Work
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	w = request(t, s.http, "POST", base+"/work/cancel", wireRequest(s.Root(), work.CancelRequest{WorkTarget: work.WorkTarget{ID: item.ID, ExpectedRevision: item.Revision + 1}, Reason: "withdrawn"}))
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", base+"/close", nil)
	r.Header.Set("Authorization", "Bearer test-token")
	r.Header.Set("Idempotency-Key", "key")
	w = httptest.NewRecorder()
	s.http.ServeHTTP(w, r)
	if w.Code != 400 || s.State() != harness.Open {
		t.Fatal(w.Code, s.State())
	}
	w = request(t, s.http, "POST", base+"/close", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, s.http, "POST", base+"/messages", httpapi.SendRequest{To: s.Root(), Content: "late"})
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func createHTTPWorker(t *testing.T, s *testSession) identity.ActorID {
	t.Helper()
	r := roster.CreateRequest{Role: roster.Implementor}
	v := operationResult(t, s, true, s.Root(), "create_agent", r, func() (roster.Registration, error) { return s.CreateAgent(context.Background(), s.Root(), r) })
	return v.AgentID
}

// wireRequest is the model-command HTTP envelope. Host-only plan updates use
// WorkRequest[work.PlanUpdate] directly and do not pass through this helper.
func wireRequest[A any](actor identity.ActorID, input A) httpapi.WorkRequest[tool.Input[A]] {
	return httpapi.WorkRequest[tool.Input[A]]{Actor: actor, Request: tool.Input[A]{Value: input}}
}
