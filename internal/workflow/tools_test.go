package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/roster"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func callAs(s *Session, ctx context.Context, actor message.ActorID, name string, args any) (tool.Result, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return tool.Result{}, err
	}
	for _, op := range s.RootTools() {
		if op.Definition().Name == name {
			return op.Call(ctx, tool.Call{Actor: actor, Arguments: raw})
		}
	}
	return tool.Result{}, errors.New("missing operation")
}
func TestWorkInspectionIncludesScopedStepsSubmissionAndAudit(t *testing.T) {
	_, s := recoverySession(t)
	created := invokeRoot(t, s, "create_plan", map[string]any{"title": "plan", "steps": []any{map[string]any{"title": "first"}, map[string]any{"title": "second"}}})
	var p work.Plan
	if err := json.Unmarshal([]byte(created.Content.Text()), &p); err != nil {
		t.Fatal(err)
	}
	value := invokeRoot(t, s, "assign_work", tool.AssignWorkArgs{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "task", Scope: &work.Scope{PlanID: p.ID, StepIDs: []work.StepID{p.Steps[0].ID}}})
	var w work.Work
	if err := json.Unmarshal([]byte(value.Content.Text()), &w); err != nil {
		t.Fatal(err)
	}
	if _, err := callAs(s, context.Background(), s.Root(), "get_plan", map[string]any{"plan_id": p.ID}); err != nil {
		t.Fatal(err)
	}
	v := invokeRoot(t, s, "get_work", map[string]any{"work_id": w.ID})
	var inspection struct {
		Work       work.Work
		Steps      []work.Step
		Submission *work.Submission
		Audit      *work.Audit
	}
	if err := json.Unmarshal([]byte(v.Content.Text()), &inspection); err != nil || len(inspection.Steps) != 1 || inspection.Steps[0].Title != "first" {
		t.Fatal(inspection, err)
	}
	receipt, err := s.Store.ReportWorkProgress(w.Assignee, work.ReportWorkProgressRequest{AssignedAtRevision: w.AssignedAtRevision, WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Steps: []work.StepProgress{{ID: p.Steps[0].ID, Status: ptr(work.ReadyForReview)}}})
	if err != nil {
		t.Fatal(err)
	}
	w.Revision = receipt.WorkRevision
	sub, err := s.Store.SubmitWork(w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	w, _ = s.Store.GetWork(s.Root(), w.ID)
	v = invokeRoot(t, s, "assign_work", tool.AssignWorkArgs{Kind: work.AuditWork, Assignee: createWorker(t, s, roster.Auditor), WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: sub.ID})
	var a work.Work
	json.Unmarshal([]byte(v.Content.Text()), &a)
	v = invokeRoot(t, s, "get_work", map[string]any{"work_id": a.ID})
	if err := json.Unmarshal([]byte(v.Content.Text()), &inspection); err != nil || inspection.Submission == nil || inspection.Submission.ID != sub.ID {
		t.Fatal(inspection, err)
	}
	audit, err := s.Store.SubmitAudit(a.Assignee, work.AuditRequest{WorkTarget: work.WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, SubmissionID: sub.ID, Verdict: work.Fail, Summary: "fix", Findings: []work.Finding{{StepIDs: []work.StepID{p.Steps[0].ID}, Description: "missing check", RequiredChange: "add check", Verification: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	w, _ = s.GetWork(context.Background(), s.Root(), w.ID)
	repairArgs := tool.AssignWorkArgs{Kind: work.Repair, Assignee: w.Assignee, WorkID: w.ID, ExpectedRevision: w.Revision, AuditID: audit.ID}
	v = invokeRoot(t, s, "assign_work", repairArgs)
	var repair work.Work
	json.Unmarshal([]byte(v.Content.Text()), &repair)
	v = invokeRoot(t, s, "get_work", map[string]any{"work_id": repair.ID})
	if err := json.Unmarshal([]byte(v.Content.Text()), &inspection); err != nil || inspection.Audit == nil || inspection.Audit.ID != audit.ID {
		t.Fatal(inspection, err)
	}
	v = invokeRoot(t, s, "get_audit", map[string]any{"audit_id": audit.ID})
	var got work.Audit
	json.Unmarshal([]byte(v.Content.Text()), &got)
	if got.ID != audit.ID {
		t.Fatal(got)
	}
	for _, name := range []string{"get_work", "get_plan", "get_audit"} {
		key := map[string]string{"get_work": "work_id", "get_plan": "plan_id", "get_audit": "audit_id"}[name]
		if _, err := callAs(s, context.Background(), s.Root(), name, map[string]any{key: "missing"}); !errors.Is(err, work.ErrNotFound) {
			t.Fatal(name, err)
		}
	}
}
func TestAssignmentAndReassignmentRejectInvalidRequests(t *testing.T) {
	ctx, s := recoverySession(t)
	w := assigned(t, s)
	for _, args := range []map[string]any{
		{"kind": "implementation", "task": "task", "work_id": "unexpected"},
		{"kind": "audit", "task": "unexpected", "work_id": w.ID, "expected_revision": w.Revision, "submission_id": "sub"},
		{"kind": "implementation", "task": "task", "assignee": "missing"},
		{"kind": "implementation", "task": "task", "scope": map[string]any{"plan_id": "missing", "step_ids": []string{"missing"}}},
		{"kind": "audit", "work_id": w.ID, "expected_revision": w.Revision, "submission_id": "missing"},
	} {
		if _, err := callAs(s, ctx, s.Root(), "assign_work", args); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if _, err := callAs(s, ctx, w.Assignee, "assign_work", map[string]any{"kind": "implementation", "assignee": w.Assignee, "task": "no delegation"}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actor message.ActorID
		args  map[string]any
	}{
		{s.Root(), map[string]any{"work_id": "missing", "expected_revision": 1}},
		{w.Assignee, map[string]any{"work_id": w.ID, "expected_revision": w.Revision}},
		{s.Root(), map[string]any{"work_id": w.ID, "expected_revision": 99}},
		{s.Root(), map[string]any{"work_id": w.ID, "expected_revision": w.Revision, "assignee": "missing"}},
	} {
		if _, err := callAs(s, ctx, tc.actor, "reassign_work", tc.args); err == nil {
			t.Fatal("accepted", tc)
		}
	}
	// Reusing a provisioned implementor is supported.
	invokeRoot(t, s, "assign_work", tool.AssignWorkArgs{Kind: work.Implementation, Task: "second", Assignee: w.Assignee})
	_, err := s.Controller.StopAgent(w.Assignee)
	if err != nil {
		t.Fatal(err)
	}
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		v, ok := e.(conversation.AgentExited)
		return ok && v.Agent == w.Assignee
	})
	if _, err := callAs(s, ctx, s.Root(), "assign_work", tool.AssignWorkArgs{Kind: work.Implementation, Task: "dead", Assignee: w.Assignee}); err == nil {
		t.Fatal("dead worker reused")
	}
	invokeRoot(t, s, "cancel_work", map[string]any{"work_id": w.ID, "expected_revision": w.Revision, "reason": "withdraw"})
	current, _ := s.Store.GetWork(s.Root(), w.ID)
	if _, err := callAs(s, ctx, s.Root(), "reassign_work", map[string]any{"assignee": w.Assignee, "work_id": w.ID, "expected_revision": current.Revision}); !errors.Is(err, work.ErrState) {
		t.Fatal(err)
	}
}
func TestAuditReassignmentUsesExplicitAuditor(t *testing.T) {
	_, s := recoverySession(t)
	w := assigned(t, s)
	sub, err := s.Store.SubmitWork(w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	w, _ = s.Store.GetWork(s.Root(), w.ID)
	v := invokeRoot(t, s, "assign_work", tool.AssignWorkArgs{Kind: work.AuditWork, Assignee: createWorker(t, s, roster.Auditor), WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: sub.ID})
	var a work.Work
	json.Unmarshal([]byte(v.Content.Text()), &a)
	v = invokeRoot(t, s, "reassign_work", map[string]any{"assignee": createWorker(t, s, roster.Auditor), "work_id": a.ID, "expected_revision": a.Revision})
	var replacement work.Work
	json.Unmarshal([]byte(v.Content.Text()), &replacement)
	if replacement.Assignee == a.Assignee || replacement.Kind != work.AuditWork {
		t.Fatal(replacement)
	}
}

func TestCreationFailsWhenConversationIsClosed(t *testing.T) {
	_, s := recoverySession(t)
	if err := s.Controller.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := callAs(s, context.Background(), s.Root(), "create_agent", roster.CreateRequest{Role: roster.Implementor}); err == nil {
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
	w, _ = s.Store.GetWork(s.Root(), w.ID)
	v := invokeRoot(t, s, "assign_work", tool.AssignWorkArgs{Kind: work.AuditWork, Assignee: createWorker(t, s, roster.Auditor), WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: sub.ID})
	var a work.Work
	json.Unmarshal([]byte(v.Content.Text()), &a)
	invokeRoot(t, s, "cancel_work", map[string]any{"work_id": a.ID, "expected_revision": a.Revision, "reason": "withdraw review"})
	if _, err := callAs(s, context.Background(), a.Assignee, "get_work", map[string]any{"work_id": a.ID}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
}

type cancelOnRegistration struct{ cancel context.CancelFunc }

func (t cancelOnRegistration) Validate() error { t.cancel(); return nil }
func (cancelOnRegistration) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "external", Parameters: json.RawMessage(`{"type":"object"}`)}
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
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: idleProvider{}, Tools: s.RootTools()}); err != nil {
		t.Fatal(err)
	}
	if _, err := callAs(s, ctx, s.Root(), "create_agent", roster.CreateRequest{Role: roster.Implementor}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	agents := s.Agents()
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
