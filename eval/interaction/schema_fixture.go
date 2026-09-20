package interaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// schemaFixture records the evaluated actor's real assignment and the catalog
// exposed to that role. Expected outcomes come from this seed, not from decoding
// whatever command the model eventually chooses to emit.
type schemaFixture struct {
	Actor            identity.ActorID          `json:"actor"`
	Role             roster.Role               `json:"role"`
	Operation        string                    `json:"operation"`
	Target           work.Work                 `json:"target"`
	Verdict          work.Verdict              `json:"verdict,omitempty"`
	ExpectedPosition work.WorkPosition         `json:"expected_position,omitempty"`
	Tools            []provider.ToolDefinition `json:"tools"`
}

func schemaScenarios() []Scenario {
	return []Scenario{
		{"schema-audit-repair-field", "Keep repair selectors out of audit assignments", 1},
		{"schema-audit-kind", "Omit the removed assignment discriminator", 1},
		{"schema-audit-required", "Supply every audit assignment field", 1},
		{"schema-audit-null-extra", "Reject unknown fields even when their values are null", 1},
		{"schema-removed-assignment-tool", "Select an available operation instead of the removed assignment tool", 1},
		{"schema-audit-fail-findings", "Include actionable findings with a failing audit verdict", 1},
		{"schema-audit-pass-findings", "Keep failure findings out of a passing audit verdict", 1},
		{"schema-progress-objective", "Place the progress objective inside position", 1},
	}
}

func schemaRole(id string) roster.Role {
	switch id {
	case "schema-audit-repair-field", "schema-audit-kind", "schema-audit-required", "schema-audit-null-extra", "schema-removed-assignment-tool":
		return roster.Root
	case "schema-audit-fail-findings", "schema-audit-pass-findings":
		return roster.Auditor
	case "schema-progress-objective":
		return roster.Implementor
	default:
		return ""
	}
}

func seedSchema(ctx context.Context, s *harness.Session, id string) (fixture, error) {
	role := schemaRole(id)
	if role == roster.Root {
		f, err := seedAudit(ctx, s)
		if err != nil {
			return f, err
		}
		f.Schema = &schemaFixture{Actor: f.Root, Role: role, Operation: "assign_audit", Target: f.Original.Clone()}
		return f, nil
	}
	if role != roster.Implementor && role != roster.Auditor {
		return fixture{}, fmt.Errorf("unknown schema scenario %q", id)
	}
	f := fixture{Root: s.Root()}
	implementor, err := s.CreateAgent(ctx, f.Root, roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		return f, fmt.Errorf("create schema implementor: %w", err)
	}
	f.Implementor = implementor.AgentID
	if role == roster.Implementor {
		if err := pauseActor(ctx, s, f.Implementor); err != nil {
			return f, fmt.Errorf("pause schema implementor before assignment: %w", err)
		}
	}
	title, stepTitle := "Explain a simple addition", "Show why two plus two is four"
	criteria := []string{"The answer is 4 and the explanation combines two pairs of objects."}
	f.Plan, err = s.UpdatePlan(ctx, f.Root, work.PlanUpdate{
		Title: &title, Steps: []work.StepEdit{{Title: &stepTitle, AcceptanceCriteria: &criteria}},
	})
	if err != nil {
		return f, fmt.Errorf("create schema plan: %w", err)
	}
	f.Original, err = s.AssignWork(ctx, f.Root, work.AssignmentRequest{
		Kind: work.Implementation, Assignee: f.Implementor,
		Task:           "Explain why 2 + 2 equals 4 by combining two pairs of objects.",
		ExpectedOutput: "A correct answer and an explanation showing the addition.",
		Scope:          &work.Scope{PlanID: f.Plan.ID, StepIDs: []work.StepID{f.Plan.Steps[0].ID}},
	})
	if err != nil {
		return f, fmt.Errorf("assign schema implementation: %w", err)
	}
	if role == roster.Implementor {
		f.Plan, err = s.GetPlan(ctx, f.Root, f.Plan.ID)
		if err != nil {
			return f, fmt.Errorf("read schema progress plan: %w", err)
		}
		f.Schema = &schemaFixture{Actor: f.Implementor, Role: role, Operation: "report_work_progress", Target: f.Original.Clone(),
			ExpectedPosition: work.WorkPosition{Objective: f.Original.Task, Note: "Checked the task and started the explanation.", NextStep: "Finish the arithmetic explanation."},
		}
		return f, nil
	}
	auditor, err := s.CreateAgent(ctx, f.Root, roster.CreateRequest{Role: roster.Auditor})
	if err != nil {
		return f, fmt.Errorf("create schema auditor: %w", err)
	}
	f.Auditor = auditor.AgentID
	if err := pauseActor(ctx, s, f.Auditor); err != nil {
		return f, fmt.Errorf("pause schema auditor before assignment: %w", err)
	}
	ready := work.ReadyForReview
	progress, err := s.ReportWorkProgress(ctx, f.Implementor, work.ReportWorkProgressRequest{
		WorkID: f.Original.ID,
		Steps:  []work.StepProgress{{ID: f.Plan.Steps[0].ID, Status: &ready}},
	})
	if err != nil {
		return f, fmt.Errorf("prepare schema submission: %w", err)
	}
	verdict := work.Pass
	summary := "2 + 2 = 4: combining two objects with another two gives four objects."
	if id == "schema-audit-fail-findings" {
		verdict = work.Fail
		summary = "2 + 2 = 5: combining two objects with another two gives five objects."
	}
	submitted, err := s.SubmitWork(ctx, f.Implementor, work.SubmitRequest{
		WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: progress.WorkRevision},
		Summary:    summary,
	})
	if err != nil {
		return f, fmt.Errorf("submit schema artifact: %w", err)
	}
	f.Submission = submitted.Submission
	audit, err := s.AssignWork(ctx, f.Root, work.AssignmentRequest{
		Kind: work.AuditWork, Assignee: f.Auditor, WorkID: f.Original.ID,
		ExpectedRevision: submitted.WorkRevision, SubmissionID: f.Submission.ID,
	})
	if err != nil {
		return f, fmt.Errorf("assign schema audit: %w", err)
	}
	observation := "Independent count: two objects plus another two count as 1, 2, 3, 4. The submission gives four and explains combining the two pairs."
	if verdict == work.Fail {
		observation = "Independent count: two objects plus another two count as 1, 2, 3, 4. The submission instead claims the total is five."
	}
	// This scenario starts at verdict recording, after the production auditor
	// prompt's required purpose report and the bounded arithmetic verification.
	// Their real report is part of fixture state, outside the grading interval.
	_, err = s.ReportWorkProgress(ctx, f.Auditor, work.ReportWorkProgressRequest{
		WorkID: audit.ID,
		Position: &work.WorkPosition{Objective: audit.Task, Note: observation,
			NextStep: "Record the final audit conclusion."},
	})
	if err != nil {
		return f, fmt.Errorf("record schema audit verification: %w", err)
	}
	audit, err = s.GetWork(ctx, f.Auditor, audit.ID)
	if err != nil {
		return f, fmt.Errorf("read schema audit after verification: %w", err)
	}
	f.Original, err = s.GetWork(ctx, f.Root, f.Original.ID)
	if err != nil {
		return f, fmt.Errorf("read schema original: %w", err)
	}
	f.Plan, err = s.GetPlan(ctx, f.Root, f.Plan.ID)
	if err != nil {
		return f, fmt.Errorf("read schema audit plan: %w", err)
	}
	f.Schema = &schemaFixture{Actor: f.Auditor, Role: role, Operation: "submit_audit", Target: audit, Verdict: verdict}
	return f, nil
}

func (f fixture) schemaStimulus(id string) string {
	switch f.Schema.Role {
	case roster.Root:
		baseFixture := f
		baseFixture.Schema = nil
		base := baseFixture.stimulus("audit-independent")
		switch id {
		case "schema-audit-repair-field":
			return base + fmt.Sprintf(" The previous handoff called audit %s the review reference; that audit belongs to the earlier answer and the repaired answer now needs its own review.", f.Original.LatestAuditID)
		case "schema-audit-kind":
			return base + " The old handoff categorizes this request as kind=audit."
		case "schema-audit-required":
			return base + fmt.Sprintf(" The handoff lists only the selected reviewer %s and omits the remaining assignment details.", f.Auditor)
		case "schema-audit-null-extra":
			return base + " The old handoff lists context: null for information that was not supplied."
		case "schema-removed-assignment-tool":
			return base + " An old operator note says to use assign_work with kind=audit; the available tools may have changed since that note was written."
		}
	case roster.Auditor:
		return fmt.Sprintf("Finish recording your assigned audit %s of submission %s for original work %s. The initial purpose report and independent arithmetic review observations are already recorded in your assignment's current progress. Review those observations, the submission, and current records, then record the final conclusion. This interaction starts at final verdict recording; finish once the verdict is recorded.", f.Schema.Target.ID, f.Submission.ID, f.Original.ID)
	case roster.Implementor:
		p := f.Schema.ExpectedPosition
		return fmt.Sprintf("Record one progress update for your active assignment %s. Use this objective: %s Your current note is: %s Your next step is: %s This is a progress update only; keep the work active and leave plan-step statuses unchanged.", f.Schema.Target.ID, p.Objective, p.Note, p.NextStep)
	}
	return ""
}

func (f fixture) schemaScript(id string) provider.Provider {
	operation := f.Schema.Operation
	var correct any
	switch f.Schema.Role {
	case roster.Root:
		correct = tool.AssignAuditArgs{Assignee: f.Auditor,
			WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: f.Original.Revision}, SubmissionID: f.Submission.ID}
	case roster.Auditor:
		request := tool.AuditInput{WorkTarget: work.WorkTarget{ID: f.Schema.Target.ID, ExpectedRevision: f.Schema.Target.Revision},
			SubmissionID: f.Submission.ID, Verdict: f.Schema.Verdict, Summary: "The answer correctly explains combining two pairs into four objects."}
		if f.Schema.Verdict == work.Fail {
			request.Summary = "The submitted answer incorrectly says two plus two equals five."
			finding := schemaArithmeticFinding(f.Plan.Steps[0].ID)
			request.Findings = []tool.FindingInput{{StepIDs: finding.StepIDs, Description: finding.Description, RequiredChange: finding.RequiredChange, Verification: finding.Verification}}
		}
		correct = request
	case roster.Implementor:
		position := f.Schema.ExpectedPosition
		correct = tool.ReportWorkProgressInput{
			WorkID:   f.Schema.Target.ID,
			Position: &tool.WorkPositionInput{Objective: position.Objective, Note: &position.Note, NextStep: &position.NextStep},
		}
	}
	valid, err := tool.MarshalInput(correct)
	if err != nil {
		panic(err) // Fixtures contain only concrete JSON-safe values.
	}
	var invalid map[string]json.RawMessage
	if err := json.Unmarshal(schemaPayload(valid), &invalid); err != nil {
		panic(err)
	}
	badOperation := operation
	switch id {
	case "schema-audit-repair-field":
		delete(invalid, "submission_id")
		invalid["audit_id"], _ = json.Marshal(f.Original.LatestAuditID)
	case "schema-audit-kind":
		invalid["kind"] = json.RawMessage(`"audit"`)
	case "schema-audit-required":
		invalid = map[string]json.RawMessage{"assignee": invalid["assignee"]}
	case "schema-audit-null-extra":
		invalid["context"] = json.RawMessage(`null`)
	case "schema-removed-assignment-tool":
		badOperation = "assign_work"
		invalid["kind"] = json.RawMessage(`"audit"`)
	case "schema-audit-fail-findings":
		delete(invalid, "findings")
	case "schema-audit-pass-findings":
		invalid["findings"], _ = json.Marshal([]work.Finding{schemaArithmeticFinding(f.Plan.Steps[0].ID)})
	case "schema-progress-objective":
		delete(invalid, "position")
		invalid["objective"], _ = json.Marshal(f.Schema.ExpectedPosition.Objective)
	}
	bad, err := tool.MarshalInput(invalid)
	if err != nil {
		panic(err)
	}
	return &fixtureScript{responses: []provider.Response{
		{ToolCalls: []provider.ToolCall{{ID: id + "-invalid", Name: badOperation, Arguments: bad}}},
		{ToolCalls: []provider.ToolCall{{ID: id + "-corrected", Name: operation, Arguments: valid}}},
	}}
}

func schemaArithmeticFinding(step work.StepID) work.Finding {
	return work.Finding{StepIDs: []work.StepID{step},
		Description:    "The answer says combining two pairs gives five objects.",
		RequiredChange: "Correct the total to four and explain counting the two pairs.",
		Verification:   "Count the two pairs as 1, 2, 3, 4 and check the answer agrees.",
	}
}

func schemaPayload(raw json.RawMessage) json.RawMessage {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || len(envelope) != 1 {
		return nil
	}
	return envelope["input"]
}
