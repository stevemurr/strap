package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stevemurr/strap/roster"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func callAs(s *Session, ctx context.Context, actor message.ActorID, name string, args any) (tool.Result, error) {
	raw, err := tool.MarshalInput(args)
	if err != nil {
		return tool.Result{}, err
	}
	for _, op := range s.CoordinationTools() {
		if op.Definition().Name == name {
			return op.Call(ctx, tool.Call{Actor: actor, Arguments: raw})
		}
	}
	return tool.Result{}, errors.New("missing operation")
}
func TestWorkInspectionIncludesScopedStepsSubmissionAndAudit(t *testing.T) {
	_, s := recoverySession(t)
	created := invokeManager(t, s, "create_plan", map[string]any{"title": "plan", "steps": []any{map[string]any{"title": "first", "acceptance_criteria": nil}, map[string]any{"title": "second", "acceptance_criteria": nil}}})
	var p work.Plan
	if err := json.Unmarshal([]byte(created.Content.Text()), &p); err != nil {
		t.Fatal(err)
	}
	value := invokeManager(t, s, "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(createWorker(t, s, roster.Implementor)), Task: "task", Scope: &work.Scope{PlanID: p.ID, StepIDs: []work.StepID{p.Steps[0].ID}}})
	var w work.Work
	if err := json.Unmarshal([]byte(value.Content.Text()), &w); err != nil {
		t.Fatal(err)
	}
	if _, err := callAs(s, context.Background(), coord(s), "get_plan", map[string]any{"plan_id": p.ID}); err != nil {
		t.Fatal(err)
	}
	v := invokeManager(t, s, "get_work", map[string]any{"work_id": w.ID})
	var inspection struct {
		Work       work.Work
		Steps      []work.Step
		Submission *work.Submission
		Audit      *work.Audit
	}
	if err := json.Unmarshal([]byte(v.Content.Text()), &inspection); err != nil || len(inspection.Steps) != 1 || inspection.Steps[0].Title != "first" {
		t.Fatal(inspection, err)
	}
	receipt, err := s.Store.ReportWorkProgress(w.Assignee, work.ReportWorkProgressRequest{WorkID: w.ID, Steps: []work.StepProgress{{ID: p.Steps[0].ID, Status: ptr(work.ReadyForReview)}}})
	if err != nil {
		t.Fatal(err)
	}
	w.Revision = receipt.WorkRevision
	sub, err := s.Store.SubmitWork(w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	w, _ = s.Store.GetWork(coord(s), w.ID)
	v = invokeManager(t, s, "assign_audit", tool.AssignAuditArgs{Assignee: createWorker(t, s, roster.Auditor), WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, SubmissionID: sub.ID})
	var a work.Work
	json.Unmarshal([]byte(v.Content.Text()), &a)
	v = invokeManager(t, s, "get_work", map[string]any{"work_id": a.ID})
	if err := json.Unmarshal([]byte(v.Content.Text()), &inspection); err != nil || inspection.Submission == nil || inspection.Submission.ID != sub.ID {
		t.Fatal(inspection, err)
	}
	audit, err := s.Store.SubmitAudit(a.Assignee, work.AuditRequest{WorkTarget: work.WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, SubmissionID: sub.ID, Verdict: work.Fail, Summary: "fix", Findings: []work.Finding{{StepIDs: []work.StepID{p.Steps[0].ID}, Description: "missing check", RequiredChange: "add check", Verification: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	w, _ = s.GetWork(context.Background(), coord(s), w.ID)
	repairArgs := tool.AssignRepairArgs{Assignee: w.Assignee, WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, AuditID: audit.ID}
	v = invokeManager(t, s, "assign_repair", repairArgs)
	var repair work.Work
	json.Unmarshal([]byte(v.Content.Text()), &repair)
	v = invokeManager(t, s, "get_work", map[string]any{"work_id": repair.ID})
	if err := json.Unmarshal([]byte(v.Content.Text()), &inspection); err != nil || inspection.Audit == nil || inspection.Audit.ID != audit.ID {
		t.Fatal(inspection, err)
	}
	v = invokeManager(t, s, "get_audit", map[string]any{"audit_id": audit.ID})
	var got work.Audit
	json.Unmarshal([]byte(v.Content.Text()), &got)
	if got.ID != audit.ID {
		t.Fatal(got)
	}
	for _, name := range []string{"get_work", "get_plan", "get_audit"} {
		key := map[string]string{"get_work": "work_id", "get_plan": "plan_id", "get_audit": "audit_id"}[name]
		if _, err := callAs(s, context.Background(), coord(s), name, map[string]any{key: "missing"}); !errors.Is(err, work.ErrNotFound) {
			t.Fatal(name, err)
		}
	}
}
func TestAssignmentRejectsInvalidRequests(t *testing.T) {
	ctx, s := recoverySession(t)
	w := assigned(t, s)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"assign_task", map[string]any{"kind": "implementation", "assignee": w.Assignee, "task": "task", "work_id": "unexpected", "context": nil, "expected_output": nil, "scope": nil}},
		{"assign_audit", map[string]any{"assignee": w.Assignee, "task": "unexpected", "work_id": w.ID, "expected_revision": w.Revision, "submission_id": "sub"}},
		{"assign_task", map[string]any{"kind": "implementation", "task": "task", "assignee": "missing", "context": nil, "expected_output": nil, "scope": nil}},
		{"assign_task", map[string]any{"kind": "implementation", "assignee": w.Assignee, "task": "task", "scope": map[string]any{"plan_id": "missing", "step_ids": []string{"missing"}}, "context": nil, "expected_output": nil}},
		{"assign_audit", map[string]any{"assignee": w.Assignee, "work_id": w.ID, "expected_revision": w.Revision, "submission_id": "missing"}},
	} {
		if _, err := callAs(s, ctx, coord(s), tc.name, tc.args); err == nil {
			t.Fatal("accepted", tc)
		}
	}
	if _, err := callAs(s, ctx, w.Assignee, "assign_task", map[string]any{"kind": "implementation", "assignee": w.Assignee, "task": "no delegation", "context": nil, "expected_output": nil, "scope": nil}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
	// Reusing a provisioned implementor is supported.
	invokeManager(t, s, "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Task: "second", Assignee: ptr(w.Assignee)})
	_, err := s.Controller.StopAgent(w.Assignee)
	if err != nil {
		t.Fatal(err)
	}
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		v, ok := e.(conversation.AgentExited)
		return ok && v.Agent == w.Assignee
	})
	if _, err := callAs(s, ctx, coord(s), "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Task: "dead", Assignee: ptr(w.Assignee)}); err == nil {
		t.Fatal("dead worker reused")
	}
	invokeManager(t, s, "cancel_work", map[string]any{"work_id": w.ID, "expected_revision": w.Revision, "reason": "withdraw"})
	if _, err := callAs(s, ctx, coord(s), "reassign_work", map[string]any{"assignee": w.Assignee, "work_id": w.ID, "expected_revision": w.Revision}); err == nil {
		t.Fatal("reassign_work is not a tool")
	}
}
func TestCreationFailsWhenConversationIsClosed(t *testing.T) {
	_, s := recoverySession(t)
	if err := s.Controller.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := callAs(s, context.Background(), coord(s), "create_agent", roster.CreateRequest{Role: roster.Implementor}); err == nil {
		t.Fatal("provisioned agent in closed conversation")
	}
}

func TestCanceledAuditCannotInspectRevokedSubmission(t *testing.T) {
	_, s := recoverySession(t)
	w := assigned(t, s)
	sub, err := s.Store.SubmitWork(w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	w, _ = s.Store.GetWork(coord(s), w.ID)
	v := invokeManager(t, s, "assign_audit", tool.AssignAuditArgs{Assignee: createWorker(t, s, roster.Auditor), WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, SubmissionID: sub.ID})
	var a work.Work
	json.Unmarshal([]byte(v.Content.Text()), &a)
	invokeManager(t, s, "cancel_work", map[string]any{"work_id": a.ID, "expected_revision": a.Revision, "reason": "withdraw review"})
	if _, err := callAs(s, context.Background(), a.Assignee, "get_work", map[string]any{"work_id": a.ID}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
}

type cancelOnRegistration struct{ cancel context.CancelFunc }

func (t cancelOnRegistration) Validate() error { t.cancel(); return nil }
func (t cancelOnRegistration) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "external", Parameters: t.InputContract().Schema()}
}
func (cancelOnRegistration) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Text("ok"), nil
}

func TestCreationCancellationStopsUnregisteredAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := conversation.New(context.Background())
	s := New(context.Background(), c, agent.Spec{Provider: idleProvider{}, Tools: []tool.Tool{cancelOnRegistration{cancel}}}, agent.Spec{Provider: idleProvider{}})
	defer s.Close(context.Background())
	s.UseManager(agent.Spec{Provider: idleProvider{}})
	if _, err := s.CreateManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := callAs(s, ctx, coord(s), "create_agent", roster.CreateRequest{Role: roster.Implementor}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	agents := s.Agents() // Manager, the canceled worker.
	if len(agents) != 2 {
		t.Fatal("worker was not provisioned", agents)
	}
	worker := agents[1]
	if worker.State != agent.StopRequested && !worker.State.Terminal() {
		t.Fatal("canceled assignment left worker active", worker)
	}
	for _, event := range s.Store.PendingEvents(0) {
		if event.Kind == work.WorkAssigned {
			t.Fatal("canceled assignment was recorded", event)
		}
	}
}

func (t cancelOnRegistration) InputContract() tool.Contract {
	p, err := tool.NewParameters[struct{}]()
	if err != nil {
		panic(err)
	}
	return p.Contract()
}
