package tool

import (
	"context"
	"fmt"

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
type addStepArgs struct {
	PlanID             work.PlanID   `json:"plan_id"`
	ExpectedRevision   work.Revision `json:"expected_revision"`
	Title              string        `json:"title"`
	AcceptanceCriteria []string      `json:"acceptance_criteria,omitempty"`
}
type editStepArgs struct {
	PlanID             work.PlanID   `json:"plan_id"`
	ExpectedRevision   work.Revision `json:"expected_revision"`
	StepID             work.StepID   `json:"step_id"`
	Title              *string       `json:"title,omitempty"`
	AcceptanceCriteria []string      `json:"acceptance_criteria,omitempty"`
}
type cancelStepsArgs struct {
	PlanID           work.PlanID   `json:"plan_id"`
	ExpectedRevision work.Revision `json:"expected_revision"`
	StepIDs          []work.StepID `json:"step_ids"`
}
type reorderStepsArgs struct {
	PlanID           work.PlanID   `json:"plan_id"`
	ExpectedRevision work.Revision `json:"expected_revision"`
	Order            []work.StepID `json:"order"`
}
type renamePlanArgs struct {
	PlanID           work.PlanID   `json:"plan_id"`
	ExpectedRevision work.Revision `json:"expected_revision"`
	Title            string        `json:"title"`
}

const (
	planRevisionHint = "Use plan_id and the revision from get_plan or the last plan receipt as expected_revision; every plan change returns the new revision."
	statusHint       = "step status is never set through plan tools; implementors report ready_for_review with report_work_progress and a passing audit completes the step"
	noteHint         = "step notes come from worker progress reports, not plan tools"
)

// PlanTools returns the root's plan tools. Creation keeps nested steps because
// models create plans reliably. Every edit is one flat operation on one plan or
// one step: there is no whole-plan snapshot to copy back, no create-or-edit
// form to choose, and no field where a step status could go. All operations
// share the store's PlanUpdate contract and its reservation and revision rules.
func PlanTools(handle Handler[work.PlanUpdate]) []Tool {
	return []Tool{CreatePlan(handle), AddStep(handle), EditStep(handle), CancelSteps(handle), ReorderSteps(handle), RenamePlan(handle)}
}

func CreatePlan(handle Handler[work.PlanUpdate]) Tool {
	return builtin("create_plan",
		"Create the shared plan with a title and its initial steps. Each step has a title and optional acceptance_criteria, an array of strings. Omit IDs, status and revision; new steps start pending. The result issues plan_id, each step_id and revision 1. Change an existing plan with add_step, edit_step, cancel_steps, reorder_steps or rename_plan, never by creating another plan.",
		func(ctx context.Context, c Call, a createPlanArgs) (Result, error) {
			steps := make([]work.StepEdit, len(a.Steps))
			for i, step := range a.Steps {
				steps[i] = work.StepEdit{Title: &step.Title}
				if step.AcceptanceCriteria != nil {
					steps[i].AcceptanceCriteria = &step.AcceptanceCriteria
				}
			}
			return handle(ctx, c, work.PlanUpdate{Title: &a.Title, Steps: steps})
		},
		MinLength("title", 1), MinItems("steps", 1), MinLength("steps[].title", 1),
		Reject("", "plan_id", "create_plan takes no plan_id; change an existing plan with add_step, edit_step, cancel_steps, reorder_steps or rename_plan"),
		Reject("", "expected_revision", "create_plan takes no revision; change an existing plan with add_step, edit_step, cancel_steps, reorder_steps or rename_plan"),
		Reject("steps[]", "step_id", "creation issues step IDs; read them from the result and use edit_step to change a step"),
		Reject("steps[]", "status", statusHint),
		Reject("steps[]", "note", noteHint))
}

func AddStep(handle Handler[work.PlanUpdate]) Tool {
	return builtin("add_step",
		"Append one new step to a plan you own, with a title and optional acceptance_criteria (array of strings). "+planRevisionHint+" The result includes the issued step_id. A title that matches a live step is rejected; edit that step instead.",
		func(ctx context.Context, c Call, a addStepArgs) (Result, error) {
			step := work.StepEdit{Title: &a.Title}
			if a.AcceptanceCriteria != nil {
				step.AcceptanceCriteria = &a.AcceptanceCriteria
			}
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Steps: []work.StepEdit{step}})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("title", 1),
		Reject("", "step_id", "add_step issues the step_id; to change an existing step use edit_step"),
		Reject("", "status", statusHint), Reject("", "note", noteHint))
}

func EditStep(handle Handler[work.PlanUpdate]) Tool {
	return builtin("edit_step",
		"Change one existing step's title or acceptance_criteria by step_id; send only the fields that change. "+planRevisionHint+" Reserved and completed steps cannot be edited.",
		func(ctx context.Context, c Call, a editStepArgs) (Result, error) {
			step := work.StepEdit{ID: &a.StepID, Title: a.Title}
			if a.AcceptanceCriteria != nil {
				step.AcceptanceCriteria = &a.AcceptanceCriteria
			}
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Steps: []work.StepEdit{step}})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("step_id", 1), MinLength("title", 1), AtLeastOne("", "title", "acceptance_criteria"),
		Reject("", "status", statusHint), Reject("", "note", noteHint))
}

func CancelSteps(handle Handler[work.PlanUpdate]) Tool {
	return builtin("cancel_steps",
		"Cancel the listed steps of a plan you own by step_id. "+planRevisionHint+" Reserved and completed steps cannot be cancelled.",
		func(ctx context.Context, c Call, a cancelStepsArgs) (Result, error) {
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Cancel: a.StepIDs})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinItems("step_ids", 1), UniqueItems("step_ids"))
}

func ReorderSteps(handle Handler[work.PlanUpdate]) Tool {
	return builtin("reorder_steps",
		"Reorder a plan you own. order must list every step_id exactly once, including cancelled and completed steps. "+planRevisionHint,
		func(ctx context.Context, c Call, a reorderStepsArgs) (Result, error) {
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Order: a.Order})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinItems("order", 1), UniqueItems("order"))
}

func RenamePlan(handle Handler[work.PlanUpdate]) Tool {
	return builtin("rename_plan",
		"Change the title of a plan you own. "+planRevisionHint,
		func(ctx context.Context, c Call, a renamePlanArgs) (Result, error) {
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Title: &a.Title})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("title", 1))
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
			return Result{}, fmt.Errorf("%w: update_work removed; use report_work_progress with assigned_at_revision", work.ErrInvalid)
		},
		Minimum("expected_revision", 1), MinLength("work_id", 1))
}

// AssignWorkArgs preserves the tool API while sharing its typed application request.
type AssignWorkArgs = work.AssignmentRequest

func AssignWork(handle Handler[AssignWorkArgs]) Tool {
	branches := []Tool{}
	for _, b := range assignmentContracts() {
		branches = append(branches, b.tool(handle))
	}
	return compose(provider.ToolDefinition{Name: "assign_work", Description: "Create NEW tracked implementation, audit, repair, or research work for a required existing assignee. To transfer an existing work item to a replacement agent, use reassign_work. Use create_agent first to create one. Implementation requires task; scope is optional. Research requires task and forbids scope, work_id, expected_revision, submission_id and audit_id. Omit work_id, expected_revision, submission_id, and audit_id for implementation, including when reusing an agent. Audit and repair use the original implementation work_id and its current expected_revision. Audit requires submission_id; repair requires the failing verdict audit_id. For audit/repair, omit task, context, expected_output, and scope: the server derives them. Returns work registration, not delivery or completion."}, branches...)
}
func SubmitWork(handle Handler[work.SubmitRequest]) Tool {
	return builtin("submit_work",
		"Submit implementation or repairs for audit. All scoped steps must be ready_for_review and your blocker cleared. The receipt's work_revision is current for any later mutation. A text reply does not submit work.",
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
	return compose(provider.ToolDefinition{Name: "submit_audit", Description: "Record pass or fail for your assigned submission. Pass permits omitted or empty findings. Fail requires nonempty findings and requests changes. The owner must explicitly assign repairs. If unable to verify, report your work blocker instead."},
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
	return Func[work.ReassignRequest]{Spec: Definition[work.ReassignRequest]{Name: "reassign_work", Description: "Replace the worker on an existing active work item. Supply only work_id, expected_revision, and the required existing assignee. Create a replacement explicitly with create_agent if needed. Uses this work item's revision. Does not create, resume, or stop agents. Old assignment updates are rejected.", Parameters: reassignmentParameters}, Invoke: func(ctx context.Context, c Call, r work.ReassignRequest) (Result, error) { return handle(ctx, c, r) }}
}

func ListWork(handle Handler[work.ListQuery]) Tool {
	type first struct {
		Assignee identity.ActorID `json:"assignee,omitempty"`
		Kind     work.Kind        `json:"kind,omitempty"`
		State    work.State       `json:"state,omitempty"`
		Limit    int              `json:"limit,omitempty"`
	}
	type next struct {
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit,omitempty"`
	}
	return compose(provider.ToolDefinition{Name: "list_work", Description: "Discover tracked work, including closed and cancelled work. Root only. Start with optional assignee, kind and state filters; continue with cursor and optional limit only. Results describe a fixed recorded snapshot. Use get_work for current details before mutations. Discovery does not guarantee exactly-once retries."},
		builtin("first_work_page", "", func(ctx context.Context, c Call, q first) (Result, error) {
			return handle(ctx, c, work.ListQuery{Assignee: q.Assignee, Kind: q.Kind, State: q.State, Limit: q.Limit})
		}, Enum("kind", "implementation", "audit", "repair", "research"), Enum("state", "active", "needs_check", "checking", "changes_requested", "accepted", "closed", "cancelled", "delivered"), Minimum("limit", 1), Maximum("limit", 100)),
		builtin("next_work_page", "", func(ctx context.Context, c Call, q next) (Result, error) {
			return handle(ctx, c, work.ListQuery{Cursor: q.Cursor, Limit: q.Limit})
		}, MinLength("cursor", 1), Minimum("limit", 1), Maximum("limit", 100)))
}

func SubmitResearch(h Handler[work.SubmitResearchRequest]) Tool {
	return builtin("submit_research", "Deliver an immutable research brief. Use current work and assignment revisions. Cite current finding IDs; proposed steps do not change the plan. Delivery ends this investigation and does not accept implementation work.", h, MinLength("work_id", 1), Minimum("expected_revision", 1), Minimum("assigned_at_revision", 1), MinLength("summary", 1), MaxItems("finding_ids", 256), UniqueItems("finding_ids"), MaxItems("open_questions", 32), MaxItems("proposed_steps", 32))
}
