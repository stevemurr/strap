package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stevemurr/strap/roster"

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
	operations := s.CoordinationTools()
	if actor != coord(s) {
		operations = s.implementor.Tools
		if s.graph.Role(actor) == roster.Auditor {
			operations = s.auditor.Tools
		}
	}
	raw, err := tool.MarshalInput(args)
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
	manager := coord(s)
	create := func(role roster.Role) roster.Registration {
		r := roster.CreateRequest{Role: role}
		return operationResult(t, s, viaTools, manager, "create_agent", r, func() (roster.Registration, error) { return s.CreateAgent(ctx, manager, r) })
	}
	implementor := create(roster.Implementor)
	planRequest := work.PlanUpdate{Title: ptr("Storage"), Steps: []work.StepEdit{{Title: ptr("Implement")}, {Title: ptr("Unassigned")}}}
	plan := operationResult(t, s, viaTools, manager, "create_plan", map[string]any{"title": "Storage", "steps": []any{map[string]any{"title": "Implement", "acceptance_criteria": nil}, map[string]any{"title": "Unassigned", "acceptance_criteria": nil}}}, func() (work.Plan, error) {
		return s.UpdatePlan(ctx, manager, planRequest)
	})
	assignment := work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "implement storage", Scope: &work.Scope{PlanID: plan.ID, StepIDs: []work.StepID{plan.Steps[0].ID}}}
	implementation := operationResult(t, s, viaTools, manager, "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(assignment.Assignee), Task: assignment.Task, Context: &assignment.Context, ExpectedOutput: &assignment.ExpectedOutput, Scope: assignment.Scope}, func() (work.Work, error) {
		return s.AssignWork(ctx, manager, assignment)
	})
	implementationID := implementation.ID
	var lastAudit work.Audit
	for _, verdict := range []work.Verdict{work.Fail, work.Pass} {
		progress := work.ReportWorkProgressRequest{WorkID: implementation.ID, Steps: []work.StepProgress{{ID: plan.Steps[0].ID, Status: ptr(work.ReadyForReview)}}}
		receipt := operationResult(t, s, viaTools, implementation.Assignee, "report_work_progress", tool.ReportWorkProgressInput{WorkID: progress.WorkID, Steps: []tool.StepProgressInput{{ID: plan.Steps[0].ID, Status: ptr(work.ReadyForReview)}}}, func() (work.ReportWorkProgressResult, error) {
			return s.ReportWorkProgress(ctx, implementation.Assignee, progress)
		})
		implementation.Revision = receipt.WorkRevision
		submissionRequest := work.SubmitRequest{WorkTarget: work.WorkTarget{ID: implementation.ID, ExpectedRevision: implementation.Revision}, Summary: "implemented", Evidence: []string{"checked"}}
		submission := operationResult(t, s, viaTools, implementation.Assignee, "submit_work", tool.SubmitInput{WorkTarget: submissionRequest.WorkTarget, Summary: submissionRequest.Summary, Evidence: submissionRequest.Evidence}, func() (work.SubmitReceipt, error) {
			return s.SubmitWork(ctx, implementation.Assignee, submissionRequest)
		})
		original, err := s.GetWork(ctx, manager, implementationID)
		if err != nil {
			t.Fatal(err)
		}
		auditor := create(roster.Auditor) // Every audit gets a new auditor.
		auditRequest := work.AssignmentRequest{Kind: work.AuditWork, Assignee: auditor.AgentID, WorkID: original.ID, ExpectedRevision: original.Revision, SubmissionID: submission.ID}
		auditing := operationResult(t, s, viaTools, manager, "assign_audit", tool.AssignAuditArgs{Assignee: auditRequest.Assignee, WorkTarget: work.WorkTarget{ID: auditRequest.WorkID, ExpectedRevision: auditRequest.ExpectedRevision}, SubmissionID: auditRequest.SubmissionID}, func() (work.Work, error) {
			return s.AssignWork(ctx, manager, auditRequest)
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
		wireAudit := tool.AuditInput{WorkTarget: verdictRequest.WorkTarget, SubmissionID: verdictRequest.SubmissionID, Verdict: verdictRequest.Verdict, Summary: verdictRequest.Summary}
		for _, f := range verdictRequest.Findings {
			wireAudit.Findings = append(wireAudit.Findings, tool.FindingInput{StepIDs: f.StepIDs, Description: f.Description, RequiredChange: f.RequiredChange, Verification: f.Verification})
		}
		lastAudit = operationResult(t, s, viaTools, auditing.Assignee, "submit_audit", wireAudit, func() (work.Audit, error) {
			return s.SubmitAudit(ctx, auditing.Assignee, verdictRequest)
		})
		if verdict == work.Fail {
			original, e := s.GetWork(ctx, manager, implementationID)
			if e != nil {
				t.Fatal(e)
			}
			repairRequest := work.AssignmentRequest{Kind: work.Repair, Assignee: implementation.Assignee, WorkID: original.ID, ExpectedRevision: original.Revision, AuditID: lastAudit.ID}
			repairWork := operationResult(t, s, viaTools, manager, "assign_repair", tool.AssignRepairArgs{Assignee: repairRequest.Assignee, WorkTarget: work.WorkTarget{ID: repairRequest.WorkID, ExpectedRevision: repairRequest.ExpectedRevision}, AuditID: repairRequest.AuditID}, func() (work.Work, error) { return s.AssignWork(ctx, manager, repairRequest) })
			repair := operationResult(t, s, viaTools, implementation.Assignee, "get_work", map[string]any{"work_id": repairWork.ID}, func() (work.Inspection, error) {
				return s.InspectWork(ctx, implementation.Assignee, repairWork.ID)
			})
			if repair.Audit == nil || repair.Audit.ID != lastAudit.ID || repair.Work.Assignee != implementation.Assignee {
				t.Fatalf("repair did not preserve findings and assignee: %+v", repair)
			}
			implementation = repair.Work
		}
	}
	final := operationResult(t, s, viaTools, manager, "get_work", map[string]any{"work_id": implementationID}, func() (work.Inspection, error) {
		return s.InspectWork(ctx, manager, implementationID)
	})
	plan = operationResult(t, s, viaTools, manager, "get_plan", map[string]any{"plan_id": plan.ID}, func() (work.Plan, error) {
		return s.GetPlan(ctx, manager, plan.ID)
	})
	audit := operationResult(t, s, viaTools, manager, "get_audit", map[string]any{"audit_id": lastAudit.ID}, func() (work.Audit, error) {
		return s.GetAudit(ctx, manager, lastAudit.ID)
	})
	if final.Work.State != work.Accepted || plan.Steps[0].Status != work.Completed || plan.Steps[1].Status != work.Pending || audit.Verdict != work.Pass {
		t.Fatalf("unexpected audit/repair outcome: %+v, %+v, %+v", final, plan, audit)
	}
	assignment = work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "assign then cancel"}
	extra := operationResult(t, s, viaTools, manager, "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(assignment.Assignee), Task: assignment.Task, Context: &assignment.Context, ExpectedOutput: &assignment.ExpectedOutput, Scope: assignment.Scope}, func() (work.Work, error) {
		return s.AssignWork(ctx, manager, assignment)
	})
	cancel := work.CancelRequest{WorkTarget: work.WorkTarget{ID: extra.ID, ExpectedRevision: extra.Revision}, Reason: "withdrawn"}
	extra = operationResult(t, s, viaTools, manager, "cancel_work", cancel, func() (work.Work, error) {
		return s.CancelWork(ctx, manager, cancel)
	})
	if extra.State != work.Cancelled {
		t.Fatal(extra)
	}
	return operationOutcome{Plan: plan, Work: final, Audit: audit, Cancelled: extra}
}

func TestTypedAndToolOperationsProduceSameAuditRepairOutcome(t *testing.T) {
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

func TestTypedOperationsEnforceAuthorityAndRevisions(t *testing.T) {
	ctx, s := recoverySession(t)
	w, err := s.AssignWork(ctx, coord(s), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "task"})
	if err != nil {
		t.Fatal(err)
	}
	plan := work.PlanUpdate{Title: ptr("unauthorized"), Steps: []work.StepEdit{{Title: ptr("step")}}}
	if _, err := s.UpdatePlan(ctx, w.Assignee, plan); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("worker created a plan: %v", err)
	}
	// The model adapter must use that same manager-only check, even if a host
	// accidentally exposes a coordination tool to a worker.
	if _, err := callAs(s, ctx, w.Assignee, "create_plan", map[string]any{"title": "unauthorized", "steps": []any{map[string]any{"title": "step", "acceptance_criteria": nil}}}); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("tool bypassed manager-only check: %v", err)
	}
	if _, err := s.AssignWork(ctx, w.Assignee, work.AssignmentRequest{Kind: work.Implementation, Task: "delegate"}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.ReportWorkProgress(ctx, coord(s), work.ReportWorkProgressRequest{WorkID: w.ID, Position: &work.WorkPosition{Objective: "wrong actor"}}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal(err)
	}
	before := len(s.Agents())
	for _, request := range []work.AssignmentRequest{
		{Kind: work.Repair, Task: "unsupported"},
		{Kind: work.Implementation, Task: "task", WorkID: w.ID},
		{Kind: work.AuditWork, WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: "submission", Task: "unexpected"},
	} {
		if _, err := s.AssignWork(ctx, coord(s), request); !errors.Is(err, work.ErrInvalid) {
			t.Fatalf("invalid assignment %v: %v", request, err)
		}
	}
	if len(s.Agents()) != before {
		t.Fatal("invalid assignment provisioned an agent")
	}
}

func TestTypedAssignmentFailurePreservesExistingAgent(t *testing.T) {
	ctx, s := recoverySession(t)
	_, err := s.AssignWork(ctx, coord(s), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "task", Scope: &work.Scope{PlanID: "missing", StepIDs: []work.StepID{"missing"}}})
	if !errors.Is(err, work.ErrNotFound) {
		t.Fatal(err)
	}
	agents := s.Agents() // Manager, the worker.
	if len(agents) != 2 || agents[1].State != agent.Idle {
		t.Fatalf("failed assignment left a live worker: %+v", agents)
	}
}

func TestTypedOperationsRespectCanceledContext(t *testing.T) {
	_, s := recoverySession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := coord(s)
	checks := []struct {
		name string
		call func() error
	}{
		{"update_plan", func() error { _, e := s.UpdatePlan(ctx, manager, work.PlanUpdate{}); return e }},
		{"assign", func() error { _, e := s.AssignWork(ctx, manager, work.AssignmentRequest{}); return e }},
		{"cancel", func() error { _, e := s.CancelWork(ctx, manager, work.CancelRequest{}); return e }},
		{"submit", func() error { _, e := s.SubmitWork(ctx, manager, work.SubmitRequest{}); return e }},
		{"audit", func() error { _, e := s.SubmitAudit(ctx, manager, work.AuditRequest{}); return e }},
		{"plan", func() error { _, e := s.GetPlan(ctx, manager, "missing"); return e }},
		{"work", func() error { _, e := s.GetWork(ctx, manager, "missing"); return e }},
		{"submission", func() error { _, e := s.GetSubmission(ctx, manager, "missing"); return e }},
		{"get_audit", func() error { _, e := s.GetAudit(ctx, manager, "missing"); return e }},
		{"inspection", func() error { _, e := s.InspectWork(ctx, manager, "missing"); return e }},
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
