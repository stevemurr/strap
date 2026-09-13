package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/roster"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// Exercise the same domain scenario through typed methods and the actual tool
// adapters. No UI reader is involved, and the idle provider never mutates work.
func operationResult[T any](t *testing.T, s *Session, viaTools bool, actor message.ActorID, name string, args any, direct func() (T, error)) T {
	t.Helper()
	var value T
	if !viaTools {
		v, err := direct()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return v
	}
	operations := s.RootTools()
	if actor != s.Root() {
		s.mu.Lock()
		kind := s.roles[actor]
		s.mu.Unlock()
		operations = s.implementor.Tools
		if kind.Role == roster.Auditor {
			operations = s.auditor.Tools
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range operations {
		if op.Definition().Name != name {
			continue
		}
		result, err := op.Call(context.Background(), tool.Call{Actor: actor, Arguments: raw})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := json.Unmarshal([]byte(result.Content.Text()), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	t.Fatalf("%s unavailable to %s", name, actor)
	return value
}

type operationOutcome struct {
	Plan      work.Plan
	Work      work.Inspection
	Audit     work.Audit
	Cancelled work.Work
}

func operationCycle(t *testing.T, viaTools bool) operationOutcome {
	t.Helper()
	ctx, s := recoverySession(t)
	root := s.Root()
	create := func(role roster.Role) roster.Registration {
		r := roster.CreateRequest{Role: role}
		return operationResult(t, s, viaTools, root, "create_agent", r, func() (roster.Registration, error) { return s.CreateAgent(ctx, root, r) })
	}
	implementor := create(roster.Implementor)
	auditor := create(roster.Auditor)
	planRequest := work.PlanUpdate{Title: ptr("Storage"), Steps: []work.StepEdit{{Title: ptr("Implement")}, {Title: ptr("Unassigned")}}}
	plan := operationResult(t, s, viaTools, root, "update_plan", planRequest, func() (work.Plan, error) {
		return s.UpdatePlan(ctx, root, planRequest)
	})
	assignment := work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "implement storage", Scope: &work.Scope{PlanID: plan.ID, StepIDs: []work.StepID{plan.Steps[0].ID}}}
	implementation := operationResult(t, s, viaTools, root, "assign_work", assignment, func() (work.Work, error) {
		return s.AssignWork(ctx, root, assignment)
	})
	implementationID := implementation.ID
	var lastAudit work.Audit
	for _, verdict := range []work.Verdict{work.Fail, work.Pass} {
		progress := work.ProgressUpdate{WorkTarget: work.WorkTarget{ID: implementation.ID, ExpectedRevision: implementation.Revision}, Steps: []work.StepProgress{{ID: plan.Steps[0].ID, Status: ptr(work.ReadyForReview)}}}
		implementation = operationResult(t, s, viaTools, implementation.Assignee, "update_plan", progress, func() (work.Work, error) {
			return s.UpdateProgress(ctx, implementation.Assignee, progress)
		})
		submissionRequest := work.SubmitRequest{WorkTarget: work.WorkTarget{ID: implementation.ID, ExpectedRevision: implementation.Revision}, Summary: "implemented", Evidence: []string{"checked"}}
		submission := operationResult(t, s, viaTools, implementation.Assignee, "submit_work", submissionRequest, func() (work.Submission, error) {
			return s.SubmitWork(ctx, implementation.Assignee, submissionRequest)
		})
		original, err := s.GetWork(ctx, root, implementationID)
		if err != nil {
			t.Fatal(err)
		}
		auditRequest := work.AssignmentRequest{Kind: work.AuditWork, Assignee: auditor.AgentID, WorkID: original.ID, ExpectedRevision: original.Revision, SubmissionID: submission.ID}
		auditing := operationResult(t, s, viaTools, root, "assign_work", auditRequest, func() (work.Work, error) {
			return s.AssignWork(ctx, root, auditRequest)
		})
		inspection := operationResult(t, s, viaTools, auditing.Assignee, "get_work", map[string]any{"work_id": auditing.ID}, func() (work.Inspection, error) {
			return s.InspectWork(ctx, auditing.Assignee, auditing.ID)
		})
		if inspection.Submission == nil || inspection.Submission.ID != submission.ID || len(inspection.Steps) != 1 {
			t.Fatalf("missing scoped submission: %+v", inspection)
		}
		verdictRequest := work.AuditRequest{WorkTarget: work.WorkTarget{ID: auditing.ID, ExpectedRevision: auditing.Revision}, SubmissionID: submission.ID, Verdict: verdict, Summary: "reviewed"}
		if verdict == work.Fail {
			verdictRequest.Findings = []work.Finding{{StepIDs: []work.StepID{plan.Steps[0].ID}, Description: "missing check", RequiredChange: "handle error", Verification: "test failure"}}
		}
		lastAudit = operationResult(t, s, viaTools, auditing.Assignee, "submit_audit", verdictRequest, func() (work.Audit, error) {
			return s.SubmitAudit(ctx, auditing.Assignee, verdictRequest)
		})
		if verdict == work.Fail {
			original, e := s.GetWork(ctx, root, implementationID)
			if e != nil {
				t.Fatal(e)
			}
			repairRequest := work.AssignmentRequest{Kind: work.Repair, Assignee: implementation.Assignee, WorkID: original.ID, ExpectedRevision: original.Revision, AuditID: lastAudit.ID}
			repairWork := operationResult(t, s, viaTools, root, "assign_work", repairRequest, func() (work.Work, error) { return s.AssignWork(ctx, root, repairRequest) })
			repair := operationResult(t, s, viaTools, implementation.Assignee, "get_work", map[string]any{"work_id": repairWork.ID}, func() (work.Inspection, error) {
				return s.InspectWork(ctx, implementation.Assignee, repairWork.ID)
			})
			if repair.Audit == nil || repair.Audit.ID != lastAudit.ID || repair.Work.Assignee != implementation.Assignee {
				t.Fatalf("repair did not preserve findings and assignee: %+v", repair)
			}
			implementation = repair.Work
		}
	}
	final := operationResult(t, s, viaTools, root, "get_work", map[string]any{"work_id": implementationID}, func() (work.Inspection, error) {
		return s.InspectWork(ctx, root, implementationID)
	})
	plan = operationResult(t, s, viaTools, root, "get_plan", map[string]any{"plan_id": plan.ID}, func() (work.Plan, error) {
		return s.GetPlan(ctx, root, plan.ID)
	})
	audit := operationResult(t, s, viaTools, root, "get_audit", map[string]any{"audit_id": lastAudit.ID}, func() (work.Audit, error) {
		return s.GetAudit(ctx, root, lastAudit.ID)
	})
	if final.Work.State != work.Accepted || plan.Steps[0].Status != work.Completed || plan.Steps[1].Status != work.Pending || audit.Verdict != work.Pass {
		t.Fatalf("unexpected audit/repair outcome: %+v, %+v, %+v", final, plan, audit)
	}
	assignment = work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "reassign then cancel"}
	extra := operationResult(t, s, viaTools, root, "assign_work", assignment, func() (work.Work, error) {
		return s.AssignWork(ctx, root, assignment)
	})
	oldAssignee := extra.Assignee
	replacement := create(roster.Implementor)
	reassign := work.ReassignRequest{Assignee: replacement.AgentID, WorkTarget: work.WorkTarget{ID: extra.ID, ExpectedRevision: extra.Revision}}
	extra = operationResult(t, s, viaTools, root, "reassign_work", reassign, func() (work.Work, error) {
		return s.ReassignWork(ctx, root, reassign)
	})
	if extra.Assignee == oldAssignee {
		t.Fatal("replacement was not provisioned")
	}
	cancel := work.CancelRequest{WorkTarget: work.WorkTarget{ID: extra.ID, ExpectedRevision: extra.Revision}, Reason: "withdrawn"}
	extra = operationResult(t, s, viaTools, root, "cancel_work", cancel, func() (work.Work, error) {
		return s.CancelWork(ctx, root, cancel)
	})
	if extra.State != work.Cancelled {
		t.Fatal(extra)
	}
	return operationOutcome{Plan: plan, Work: final, Audit: audit, Cancelled: extra}
}

func TestTypedAndToolOperationsProduceSameAuditRepairOutcome(t *testing.T) {
	direct := operationCycle(t, false)
	adapted := operationCycle(t, true)
	if !reflect.DeepEqual(direct, adapted) {
		t.Fatalf("typed operations and tool adapters diverged:\ndirect: %#v\nadapted: %#v", direct, adapted)
	}
}

func TestTypedOperationsEnforceAuthorityAndRevisions(t *testing.T) {
	ctx, s := recoverySession(t)
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "task"})
	if err != nil {
		t.Fatal(err)
	}
	plan := work.PlanUpdate{Title: ptr("unauthorized"), Steps: []work.StepEdit{{Title: ptr("step")}}}
	if _, err := s.UpdatePlan(ctx, w.Assignee, plan); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("worker created root plan: %v", err)
	}
	// The model adapter must use that same root-only check, even if a host
	// accidentally exposes the root tool to a worker.
	if _, err := callAs(s, ctx, w.Assignee, "update_plan", plan); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("tool bypassed root-only check: %v", err)
	}
	if _, err := s.AssignWork(ctx, w.Assignee, work.AssignmentRequest{Kind: work.Implementation, Task: "delegate"}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.UpdateProgress(ctx, s.Root(), work.ProgressUpdate{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Note: ptr("wrong actor")}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
	before := len(s.Agents())
	if _, err := s.ReassignWork(ctx, s.Root(), work.ReassignRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision + 1}}); !errors.Is(err, work.ErrConflict) {
		t.Fatal(err)
	}
	if len(s.Agents()) != before {
		t.Fatal("stale reassignment provisioned an agent")
	}
	for _, request := range []work.AssignmentRequest{
		{Kind: work.Repair, Task: "unsupported"},
		{Kind: work.Implementation, Task: "task", WorkID: w.ID},
		{Kind: work.AuditWork, WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: "submission", Task: "unexpected"},
	} {
		if _, err := s.AssignWork(ctx, s.Root(), request); !errors.Is(err, work.ErrInvalid) {
			t.Fatalf("invalid assignment %v: %v", request, err)
		}
	}
	if len(s.Agents()) != before {
		t.Fatal("invalid assignment provisioned an agent")
	}
}

func TestTypedAssignmentFailurePreservesExistingAgent(t *testing.T) {
	ctx, s := recoverySession(t)
	_, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "task", Scope: &work.Scope{PlanID: "missing", StepIDs: []work.StepID{"missing"}}})
	if !errors.Is(err, work.ErrNotFound) {
		t.Fatal(err)
	}
	agents := s.Agents()
	if len(agents) != 2 || agents[1].State != agent.Idle {
		t.Fatalf("failed assignment left a live worker: %+v", agents)
	}
}

func TestTypedReassignmentCancellationPreservesOriginalBinding(t *testing.T) {
	_, s := recoverySession(t)
	w, err := s.AssignWork(context.Background(), s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "task"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replacement := createWorker(t, s, roster.Implementor)
	cancel()
	_, err = s.ReassignWork(ctx, s.Root(), work.ReassignRequest{Assignee: replacement, WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	current, err := s.GetWork(context.Background(), s.Root(), w.ID)
	if err != nil || current.Assignee != w.Assignee || current.Revision != w.Revision {
		t.Fatalf("canceled replacement changed original binding: %+v, %v", current, err)
	}
	agents := s.Agents()
	if len(agents) != 3 || agents[2].State != agent.Idle {
		t.Fatalf("canceled replacement left a live agent: %+v", agents)
	}
}

func TestTypedOperationsRespectCanceledContext(t *testing.T) {
	_, s := recoverySession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := s.Root()
	checks := []struct {
		name string
		call func() error
	}{
		{"update_plan", func() error { _, e := s.UpdatePlan(ctx, root, work.PlanUpdate{}); return e }},
		{"update_progress", func() error { _, e := s.UpdateProgress(ctx, root, work.ProgressUpdate{}); return e }},
		{"assign", func() error { _, e := s.AssignWork(ctx, root, work.AssignmentRequest{}); return e }},
		{"reassign", func() error { _, e := s.ReassignWork(ctx, root, work.ReassignRequest{}); return e }},
		{"cancel", func() error { _, e := s.CancelWork(ctx, root, work.CancelRequest{}); return e }},
		{"submit", func() error { _, e := s.SubmitWork(ctx, root, work.SubmitRequest{}); return e }},
		{"audit", func() error { _, e := s.SubmitAudit(ctx, root, work.AuditRequest{}); return e }},
		{"plan", func() error { _, e := s.GetPlan(ctx, root, "missing"); return e }},
		{"work", func() error { _, e := s.GetWork(ctx, root, "missing"); return e }},
		{"submission", func() error { _, e := s.GetSubmission(ctx, root, "missing"); return e }},
		{"get_audit", func() error { _, e := s.GetAudit(ctx, root, "missing"); return e }},
		{"inspection", func() error { _, e := s.InspectWork(ctx, root, "missing"); return e }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
	if len(s.Agents()) != 1 {
		t.Fatal("canceled operation provisioned an agent")
	}
}

func TestEmptySessionCannotAuthorizeEmptyActor(t *testing.T) {
	c := conversation.New(context.Background())
	s := New(context.Background(), c, agent.Spec{Provider: idleProvider{}}, agent.Spec{Provider: idleProvider{}})
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.AssignWork(context.Background(), "", work.AssignmentRequest{Kind: work.Implementation, Task: "task"}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
}
