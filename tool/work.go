package tool

import (
	"context"
	"strings"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type newPlanStep struct {
	Title              string   `json:"title"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}
type createPlanArgs struct {
	Title string        `json:"title"`
	Steps []newPlanStep `json:"steps"`
}
type editPlanArgs struct {
	PlanID           work.PlanID     `json:"plan_id"`
	ExpectedRevision work.Revision   `json:"expected_revision"`
	Title            *string         `json:"title,omitempty"`
	Steps            []work.StepEdit `json:"steps,omitempty"`
	Order            []work.StepID   `json:"order,omitempty"`
	Cancel           []work.StepID   `json:"cancel,omitempty"`
}

// UpdatePlan composes independently typed creation, structural editing and
// progress operations. Nil callbacks omit that capability from the tool schema.
func UpdatePlan(plan Handler[work.PlanUpdate], progress Handler[work.ProgressUpdate]) Tool {
	branches := []Tool{}
	guidance := []string{"Use exactly one operation. Send steps as a JSON array of objects and expected_revision as a JSON integer, never quoted strings. Omit unused fields; do not send null or copy whole step snapshots."}
	if plan != nil {
		guidance = append(guidance, `Create: {"title":"Plan title","steps":[{"title":"Step title"}]}. Omit IDs, revision, status and note; new steps start pending. Edit: {"plan_id":"<plan_id>","expected_revision":1,"steps":[{"step_id":"<step_id>","title":"Revised title"}]}. Use the revision from get_plan, not a work revision. Step edits allow only step_id, title and acceptance_criteria (an array of strings). New steps omit step_id. Omit unchanged steps, especially reserved or completed steps. order must list every step ID exactly once; to reorder newly added steps, first read their issued IDs from the update result. cancel lists step IDs.`)
		createPlan := func(ctx context.Context, c Call, a createPlanArgs) (Result, error) {
			steps := make([]work.StepEdit, len(a.Steps))
			for i, step := range a.Steps {
				steps[i] = work.StepEdit{Title: &step.Title}
				if step.AcceptanceCriteria != nil {
					steps[i].AcceptanceCriteria = &step.AcceptanceCriteria
				}
			}
			return plan(ctx, c, work.PlanUpdate{Title: &a.Title, Steps: steps})
		}
		editPlan := func(ctx context.Context, c Call, a editPlanArgs) (Result, error) {
			return plan(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Title: a.Title, Steps: a.Steps, Order: a.Order, Cancel: a.Cancel})
		}
		branches = append(branches,
			builtin("create_plan", "Create a plan with a title and new steps. Omit all IDs.", createPlan,
				MinLength("title", 1), MinItems("steps", 1), MinLength("steps[].title", 1)),
			builtin("edit_plan", "Edit owned structure using plan_id and expected_revision. Existing steps use step_id; new steps require title.", editPlan,
				MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("title", 1), MinLength("steps[].step_id", 1), MinLength("steps[].title", 1), AtLeastOne("steps[]", "step_id", "title"), UniqueItems("order"), UniqueItems("cancel")))
	}
	if progress != nil {
		guidance = append(guidance, `Progress: {"work_id":"<work_id>","expected_revision":1,"steps":[{"step_id":"<step_id>","status":"in_progress"}]}. Use work.revision from get_work, not the plan revision or assigned_at_revision. Each successful update returns a new work revision; use that revision for the next mutation, including submit_work. Each step allows step_id, optional status and optional note; never title. Status is pending, in_progress, blocked or ready_for_review. Top-level note and blocker update the work; an empty blocker clears it. Only audits complete work.`)
		branches = append(branches, builtin("update_progress",
			"Update assigned scoped progress using work_id and expected_revision. Only an audit can complete steps.",
			progress,
			MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("steps[].step_id", 1), Enum("steps[].status", "pending", "in_progress", "blocked", "ready_for_review")))
	}
	return compose(provider.ToolDefinition{Name: "update_plan", Description: strings.Join(guidance, " ")}, branches...)
}

// UpdateWork exposes work-level notes/blockers without implementation step fields.
func UpdateWork(handle Handler[work.ProgressUpdate]) Tool {
	type args struct {
		work.WorkTarget
		Note    *string `json:"note,omitempty"`
		Blocker *string `json:"blocker,omitempty"`
	}
	return builtin("update_work",
		"Report a note or blocker on your assigned work. A blocker reports inability to verify; it is not a verdict.",
		func(ctx context.Context, c Call, a args) (Result, error) {
			return handle(ctx, c, work.ProgressUpdate{WorkTarget: a.WorkTarget, Note: a.Note, Blocker: a.Blocker})
		},
		Minimum("expected_revision", 1), MinLength("work_id", 1))
}

// AssignWorkArgs preserves the tool API while sharing its typed application request.
type AssignWorkArgs = work.AssignmentRequest

func AssignWork(handle Handler[AssignWorkArgs]) Tool {
	type implementation struct {
		Kind           work.Kind        `json:"kind"`
		Assignee       identity.ActorID `json:"assignee,omitempty"`
		Task           string           `json:"task"`
		Context        string           `json:"context,omitempty"`
		ExpectedOutput string           `json:"expected_output,omitempty"`
		Scope          *work.Scope      `json:"scope,omitempty"`
	}
	type audit struct {
		Kind             work.Kind         `json:"kind"`
		Assignee         identity.ActorID  `json:"assignee,omitempty"`
		WorkID           work.ID           `json:"work_id"`
		ExpectedRevision work.Revision     `json:"expected_revision"`
		SubmissionID     work.SubmissionID `json:"submission_id"`
	}
	return compose(provider.ToolDefinition{Name: "assign_work", Description: "Assign implementation work or an audit. The application supplies agent configuration. Omit assignee to provision an agent."},
		// A Compose branch carries no description of its own; compose emits one
		// into the branch schema only when non-empty.
		builtin("assign_implementation", "",
			func(ctx context.Context, c Call, a implementation) (Result, error) {
				return handle(ctx, c, AssignWorkArgs{Kind: a.Kind, Assignee: a.Assignee, Task: a.Task, Context: a.Context, ExpectedOutput: a.ExpectedOutput, Scope: a.Scope})
			},
			Enum("kind", "implementation"), MinLength("task", 1), MinLength("assignee", 1), MinLength("scope.plan_id", 1), MinItems("scope.step_ids", 1), UniqueItems("scope.step_ids"), MinLength("scope.step_ids[]", 1)),
		builtin("assign_audit", "",
			func(ctx context.Context, c Call, a audit) (Result, error) {
				return handle(ctx, c, AssignWorkArgs{Kind: a.Kind, Assignee: a.Assignee, WorkID: a.WorkID, ExpectedRevision: a.ExpectedRevision, SubmissionID: a.SubmissionID})
			},
			Enum("kind", "audit"), MinLength("assignee", 1), MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("submission_id", 1)),
	)
}
func SubmitWork(handle Handler[work.SubmitRequest]) Tool {
	return builtin("submit_work",
		"Submit implementation or repairs for audit. All scoped steps must be ready_for_review and your blocker cleared. A text reply does not submit work.",
		handle, Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("summary", 1), MinLength("artifacts[].uri", 1))
}
func SubmitAudit(handle Handler[work.AuditRequest]) Tool {
	// fail is local because it drops omitempty on findings, making it required in
	// the branch schema. A pass branch is work.AuditRequest exactly, so it reuses it.
	type fail struct {
		work.WorkTarget
		SubmissionID work.SubmissionID `json:"submission_id"`
		Verdict      work.Verdict      `json:"verdict"`
		Summary      string            `json:"summary"`
		Findings     []work.Finding    `json:"findings"`
	}
	return compose(provider.ToolDefinition{Name: "submit_audit", Description: "Record pass or fail for your assigned submission. Pass permits omitted or empty findings. Fail requires nonempty findings and issues scoped repairs. If unable to verify, report your work blocker instead."},
		// The wrapper is load-bearing: compose validates branches eagerly, so a
		// composed branch needs a non-nil Invoke even when handle is nil. Passing
		// handle directly panics at construction (tool/work_test.go:162).
		builtin("pass_audit", "",
			func(ctx context.Context, c Call, a work.AuditRequest) (Result, error) { return handle(ctx, c, a) },
			Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("submission_id", 1), Enum("verdict", "pass"), MinLength("summary", 1), MaxItems("findings", 0)),
		builtin("fail_audit", "",
			func(ctx context.Context, c Call, a fail) (Result, error) {
				return handle(ctx, c, work.AuditRequest{WorkTarget: a.WorkTarget, SubmissionID: a.SubmissionID, Verdict: a.Verdict, Summary: a.Summary, Findings: a.Findings})
			},
			Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("submission_id", 1), Enum("verdict", "fail"), MinLength("summary", 1), MinItems("findings", 1), MinLength("findings[].description", 1), MinLength("findings[].required_change", 1), MinLength("findings[].verification", 1)),
	)
}
func GetWork(handle func(context.Context, Call, work.ID) (Result, error)) Tool {
	type args struct {
		ID work.ID `json:"work_id"`
	}
	return builtin("get_work",
		"Read current work, scoped steps, submission and repair findings. Use work.revision as expected_revision for work mutations; assigned_at_revision is only the assignment binding. Returned steps are snapshots, not update patches.",
		func(ctx context.Context, c Call, a args) (Result, error) { return handle(ctx, c, a.ID) },
		MinLength("work_id", 1))
}
func GetPlan(handle func(context.Context, Call, work.PlanID) (Result, error)) Tool {
	type args struct {
		ID work.PlanID `json:"plan_id"`
	}
	return builtin("get_plan",
		"Read a plan; delegates receive their authorized subset. Use revision as expected_revision for structural plan edits only. Work progress uses the separate work.revision from get_work. Returned steps include read-only status; do not copy whole snapshots into structural edits.",
		func(ctx context.Context, c Call, a args) (Result, error) { return handle(ctx, c, a.ID) },
		MinLength("plan_id", 1))
}
func GetAudit(handle func(context.Context, Call, work.AuditID) (Result, error)) Tool {
	type args struct {
		ID work.AuditID `json:"audit_id"`
	}
	return builtin("get_audit",
		"Read the recorded verdict, summary and findings for an audit_id from a work event.",
		func(ctx context.Context, c Call, a args) (Result, error) { return handle(ctx, c, a.ID) },
		MinLength("audit_id", 1))
}
func CancelWork(handle Handler[work.CancelRequest]) Tool {
	return builtin("cancel_work",
		"Owner cancels work. Cancelling implementation or repair ends that cycle; cancelling audit returns its submission for another review.",
		handle, MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("reason", 1))
}
func ReassignWork(handle Handler[work.ReassignRequest]) Tool {
	type args struct {
		work.WorkTarget
		Assignee identity.ActorID `json:"assignee,omitempty"`
	}
	return builtin("reassign_work",
		"Owner reassigns active work. Omit assignee to provision a replacement. Old assignment updates are rejected.",
		func(ctx context.Context, c Call, a args) (Result, error) {
			return handle(ctx, c, work.ReassignRequest{WorkTarget: a.WorkTarget, Assignee: a.Assignee})
		},
		MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("assignee", 1))
}
