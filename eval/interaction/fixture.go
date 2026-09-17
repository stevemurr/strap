package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// fixture is a real, unaccepted review cycle. The superseded submission is
// created by a failed audit and repair rather than by mutating ledger state.
type fixture struct {
	Root               identity.ActorID
	Implementor        identity.ActorID
	Auditor            identity.ActorID
	Plan               work.Plan
	Original           work.Work
	Submission         work.Submission
	PreviousSubmission work.SubmissionID
	Schema             *schemaFixture `json:"schema,omitempty"`
}

func (f fixture) actor() identity.ActorID {
	if f.Schema != nil {
		return f.Schema.Actor
	}
	return f.Root
}

// seedAudit requires the caller to gate the root and block collaborator model
// providers. Public host operations seed the ledger without model-side effects.
func seedAudit(ctx context.Context, s *harness.Session) (fixture, error) {
	f := fixture{Root: s.Root()}
	implementor, err := s.CreateAgent(ctx, f.Root, roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		return f, fmt.Errorf("create implementor: %w", err)
	}
	f.Implementor = implementor.AgentID
	auditor, err := s.CreateAgent(ctx, f.Root, roster.CreateRequest{Role: roster.Auditor})
	if err != nil {
		return f, fmt.Errorf("create auditor: %w", err)
	}
	f.Auditor = auditor.AgentID
	title, stepTitle := "Verify arithmetic explanation", "Explain why 2 + 2 equals 4"
	criteria := []string{"The answer is 4 and the explanation shows the addition."}
	f.Plan, err = s.UpdatePlan(ctx, f.Root, work.PlanUpdate{
		Title: &title,
		Steps: []work.StepEdit{{Title: &stepTitle, AcceptanceCriteria: &criteria}},
	})
	if err != nil {
		return f, fmt.Errorf("create plan: %w", err)
	}
	f.Original, err = s.AssignWork(ctx, f.Root, work.AssignmentRequest{
		Kind: work.Implementation, Assignee: f.Implementor,
		Task:           "Calculate 2 + 2 and explain the addition.",
		ExpectedOutput: "The answer and a short explanation of the addition.",
		Scope:          &work.Scope{PlanID: f.Plan.ID, StepIDs: []work.StepID{f.Plan.Steps[0].ID}},
	})
	if err != nil {
		return f, fmt.Errorf("assign implementation: %w", err)
	}
	ready := work.ReadyForReview
	progress, err := s.ReportWorkProgress(ctx, f.Implementor, work.ReportWorkProgressRequest{
		WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: f.Original.Revision},
		Position:   &work.WorkPosition{Objective: f.Original.Task, Note: "The answer is ready for review.", NextStep: "Submit the answer."},
		Steps:      []work.StepProgress{{ID: f.Plan.Steps[0].ID, Status: &ready}},
	})
	if err != nil {
		return f, fmt.Errorf("prepare first submission: %w", err)
	}
	f.Original.Revision = progress.WorkRevision
	first, err := s.SubmitWork(ctx, f.Implementor, work.SubmitRequest{
		WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: f.Original.Revision},
		Summary:    "4",
	})
	if err != nil {
		return f, fmt.Errorf("submit first outcome: %w", err)
	}
	f.PreviousSubmission = first.ID
	auditWork, err := s.AssignWork(ctx, f.Root, work.AssignmentRequest{
		Kind: work.AuditWork, Assignee: f.Auditor, WorkID: f.Original.ID,
		ExpectedRevision: first.WorkRevision, SubmissionID: first.ID,
	})
	if err != nil {
		return f, fmt.Errorf("assign first audit: %w", err)
	}
	audit, err := s.SubmitAudit(ctx, f.Auditor, work.AuditRequest{
		WorkTarget:   work.WorkTarget{ID: auditWork.ID, ExpectedRevision: auditWork.Revision},
		SubmissionID: first.ID, Verdict: work.Fail, Summary: "The explanation is missing.",
		Findings: []work.Finding{{
			StepIDs:        []work.StepID{f.Plan.Steps[0].ID},
			Description:    "The answer gives 4 without explaining the addition.",
			RequiredChange: "Include the addition of two pairs.",
			Verification:   "Read the replacement answer and check that the explanation supports 4.",
		}},
	})
	if err != nil {
		return f, fmt.Errorf("fail first audit: %w", err)
	}
	f.Original, err = s.GetWork(ctx, f.Root, f.Original.ID)
	if err != nil {
		return f, fmt.Errorf("read failed original: %w", err)
	}
	repair, err := s.AssignWork(ctx, f.Root, work.AssignmentRequest{
		Kind: work.Repair, Assignee: f.Implementor, WorkID: f.Original.ID,
		ExpectedRevision: f.Original.Revision, AuditID: audit.ID,
	})
	if err != nil {
		return f, fmt.Errorf("assign repair: %w", err)
	}
	progress, err = s.ReportWorkProgress(ctx, f.Implementor, work.ReportWorkProgressRequest{
		WorkTarget: work.WorkTarget{ID: repair.ID, ExpectedRevision: repair.Revision},
		Position:   &work.WorkPosition{Objective: repair.Task, Note: "The explanation now describes combining two pairs.", NextStep: "Submit the repaired answer."},
		Steps:      []work.StepProgress{{ID: f.Plan.Steps[0].ID, Status: &ready}},
	})
	if err != nil {
		return f, fmt.Errorf("prepare replacement submission: %w", err)
	}
	repair.Revision = progress.WorkRevision
	latest, err := s.SubmitWork(ctx, f.Implementor, work.SubmitRequest{
		WorkTarget: work.WorkTarget{ID: repair.ID, ExpectedRevision: repair.Revision},
		Summary:    "2 + 2 = 4: combining two objects with another two gives four objects.",
		Evidence:   []string{"Count the two pairs: 1, 2, 3, 4."},
	})
	if err != nil {
		return f, fmt.Errorf("submit replacement outcome: %w", err)
	}
	f.Submission = latest.Submission
	f.Original, err = s.GetWork(ctx, f.Root, f.Original.ID)
	if err != nil {
		return f, fmt.Errorf("read current original: %w", err)
	}
	f.Plan, err = s.GetPlan(ctx, f.Root, f.Plan.ID)
	if err != nil {
		return f, fmt.Errorf("read current plan: %w", err)
	}
	return f, nil
}

func (f fixture) stimulus(scenarioID string) string {
	if f.Schema != nil {
		return f.schemaStimulus(scenarioID)
	}
	base := fmt.Sprintf("Arrange an independent audit of the current submitted answer for work %s. The implementor is %s; an existing independent auditor is %s. Use the current work and submission records. Finish this interaction once the audit is assigned; leave the verdict to the auditor.", f.Original.ID, f.Implementor, f.Auditor)
	switch scenarioID {
	case "audit-wrong-assignee":
		return base + fmt.Sprintf(" An old handoff suggested %s as the reviewer, but that actor wrote the answer. Ensure the reviewer is independent.", f.Implementor)
	case "audit-old-submission":
		return base + fmt.Sprintf(" An outdated handoff cites submission %s; a repaired answer has since replaced it. Arrange review of the latest answer.", f.PreviousSubmission)
	case "audit-stale-revision":
		return base + fmt.Sprintf(" An outdated handoff records work revision %d; the work has changed since that handoff. Use its current revision.", f.Original.Revision-1)
	default:
		return base
	}
}

func (f fixture) script(scenarioID string) provider.Provider {
	if f.Schema != nil {
		return f.schemaScript(scenarioID)
	}
	correct := tool.AssignAuditArgs{
		Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: f.Original.Revision},
		SubmissionID: f.Submission.ID,
	}
	if scenarioID == "audit-revision-race" {
		call := func(id, name string, value any) provider.Response {
			arguments, _ := json.Marshal(value)
			return provider.Response{ToolCalls: []provider.ToolCall{{ID: id, Name: name, Arguments: arguments}}}
		}
		read := map[string]any{"work_id": f.Original.ID}
		stale := correct
		// The competing assignment and cancellation each advance the revision.
		correct.ExpectedRevision += 2
		return &fixtureScript{responses: []provider.Response{
			call("race-read", "get_work", read),
			call("race-stale", "assign_audit", stale),
			call("race-refresh", "get_work", read),
			call("race-correct", "assign_audit", correct),
		}}
	}
	var requests []tool.AssignAuditArgs
	incorrect := correct
	switch scenarioID {
	case "audit-wrong-assignee":
		incorrect.Assignee = f.Implementor
		requests = append(requests, incorrect)
	case "audit-old-submission":
		incorrect.SubmissionID = f.PreviousSubmission
		requests = append(requests, incorrect)
	case "audit-stale-revision":
		incorrect.ExpectedRevision--
		requests = append(requests, incorrect)
	}
	requests = append(requests, correct)
	responses := make([]provider.Response, len(requests))
	for i, request := range requests {
		// AssignAuditArgs contains only JSON-safe concrete values.
		arguments, _ := json.Marshal(request)
		responses[i] = provider.Response{ToolCalls: []provider.ToolCall{{
			ID: fmt.Sprintf("%s-%d", scenarioID, i+1), Name: "assign_audit", Arguments: arguments,
		}}}
	}
	return &fixtureScript{responses: responses}
}

// The runtime calls again only after consuming the preceding tool batch. Once
// exhausted, the script blocks, giving the runner a stable interaction boundary.
type fixtureScript struct {
	mu        sync.Mutex
	responses []provider.Response
	next      int
}

func (s *fixtureScript) Submit(ctx context.Context, _ provider.Request, _ provider.Observer) (provider.Response, error) {
	s.mu.Lock()
	if s.next < len(s.responses) {
		response := s.responses[s.next]
		s.next++
		s.mu.Unlock()
		return response, nil
	}
	s.mu.Unlock()
	<-ctx.Done()
	return provider.Response{}, ctx.Err()
}
