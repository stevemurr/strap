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
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}
type createPlanArgs struct {
	Title *string       `json:"title"` // Defaults to the first step's title.
	Steps []newPlanStep `json:"steps"`
}
type addStepArgs struct {
	PlanID             work.PlanID   `json:"plan_id"`
	ExpectedRevision   work.Revision `json:"expected_revision"`
	Title              string        `json:"title"`
	AcceptanceCriteria []string      `json:"acceptance_criteria"`
}
type editStepArgs struct {
	PlanID             work.PlanID   `json:"plan_id"`
	ExpectedRevision   work.Revision `json:"expected_revision"`
	StepID             work.StepID   `json:"step_id"`
	Title              *string       `json:"title"`
	AcceptanceCriteria []string      `json:"acceptance_criteria"`
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
		"Create the shared plan with its initial steps and a nullable title; a null title is taken from the first step. Each step has a title and nullable acceptance_criteria, an array of strings. Omit IDs, status and revision; new steps start pending. Each step is a unit of work you will assign; a step completes only when an audit accepts the work that includes it. The result issues plan_id, each step_id and revision 1. Change an existing plan with add_step, edit_step, cancel_steps, reorder_steps or rename_plan, never by creating another plan.",
		func(ctx context.Context, c Call, a createPlanArgs) (Result, error) {
			steps := make([]work.StepEdit, len(a.Steps))
			for i, step := range a.Steps {
				steps[i] = work.StepEdit{Title: &step.Title}
				if step.AcceptanceCriteria != nil {
					steps[i].AcceptanceCriteria = &step.AcceptanceCriteria
				}
			}
			// A null title selects the first step title explicitly.
			title := strings.TrimSpace(valueOrZero(a.Title))
			if title == "" {
				title = a.Steps[0].Title
			}
			return handle(ctx, c, work.PlanUpdate{Title: &title, Steps: steps})
		},
		Nullable("title", "use the first step title"), Nullable("steps[].acceptance_criteria", "no acceptance criteria"), MinItems("steps", 1), MinLength("steps[].title", 1),
		Reject("", "plan_id", "create_plan takes no plan_id; change an existing plan with add_step, edit_step, cancel_steps, reorder_steps or rename_plan"),
		Reject("", "expected_revision", "create_plan takes no revision; change an existing plan with add_step, edit_step, cancel_steps, reorder_steps or rename_plan"),
		Reject("steps[]", "step_id", "creation issues step IDs; read them from the result and use edit_step to change a step"),
		Reject("steps[]", "status", statusHint),
		Reject("steps[]", "note", noteHint))
}

func AddStep(handle Handler[work.PlanUpdate]) Tool {
	return builtin("add_step",
		"Append one new step to a plan you own, with a title and nullable acceptance_criteria (array of strings). "+planRevisionHint+" The result includes the issued step_id. A title that matches a live step is rejected; edit that step instead.",
		func(ctx context.Context, c Call, a addStepArgs) (Result, error) {
			step := work.StepEdit{Title: &a.Title}
			if a.AcceptanceCriteria != nil {
				step.AcceptanceCriteria = &a.AcceptanceCriteria
			}
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Steps: []work.StepEdit{step}})
		},
		Nullable("acceptance_criteria", "no acceptance criteria"), MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("title", 1),
		Reject("", "step_id", "add_step issues the step_id; to change an existing step use edit_step"),
		Reject("", "status", statusHint), Reject("", "note", noteHint)).Bookkeeping("expected_revision")
}

func EditStep(handle Handler[work.PlanUpdate]) Tool {
	return builtin("edit_step",
		"Change one existing step's title or acceptance_criteria by step_id; use null for unchanged fields. "+planRevisionHint+" Reserved and completed steps cannot be edited.",
		func(ctx context.Context, c Call, a editStepArgs) (Result, error) {
			step := work.StepEdit{ID: &a.StepID, Title: a.Title}
			if a.AcceptanceCriteria != nil {
				step.AcceptanceCriteria = &a.AcceptanceCriteria
			}
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Steps: []work.StepEdit{step}})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("step_id", 1), MinLength("title", 1), Nullable("title", "keep the current title"), Nullable("acceptance_criteria", "keep current criteria; an empty array clears them"), AtLeastOneNonNull("", "title", "acceptance_criteria"),
		Reject("", "status", statusHint), Reject("", "note", noteHint)).Bookkeeping("expected_revision")
}

func CancelSteps(handle Handler[work.PlanUpdate]) Tool {
	return builtin("cancel_steps",
		"Cancel the listed steps of a plan you own by step_id. "+planRevisionHint+" Reserved and completed steps cannot be cancelled.",
		func(ctx context.Context, c Call, a cancelStepsArgs) (Result, error) {
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Cancel: a.StepIDs})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinItems("step_ids", 1)).Bookkeeping("expected_revision")
}

func ReorderSteps(handle Handler[work.PlanUpdate]) Tool {
	return builtin("reorder_steps",
		"Reorder a plan you own. order must list every step_id exactly once, including cancelled and completed steps. "+planRevisionHint,
		func(ctx context.Context, c Call, a reorderStepsArgs) (Result, error) {
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Order: a.Order})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinItems("order", 1)).Bookkeeping("expected_revision")
}

func RenamePlan(handle Handler[work.PlanUpdate]) Tool {
	return builtin("rename_plan",
		"Change the title of a plan you own. "+planRevisionHint,
		func(ctx context.Context, c Call, a renamePlanArgs) (Result, error) {
			return handle(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Title: &a.Title})
		},
		MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("title", 1)).Bookkeeping("expected_revision")
}

func SubmitWork(handle Handler[work.SubmitRequest]) Tool {
	return Func[SubmitInput]{Spec: submitWorkDefinition, Invoke: func(ctx context.Context, c Call, a SubmitInput) (Result, error) { return handle(ctx, c, a.domain()) }}
}
func SubmitAudit(handle Handler[work.AuditRequest]) Tool {
	return composeBy("verdict", provider.ToolDefinition{Name: "submit_audit", Description: "Record pass or fail for your assigned submission. Pass permits null or empty findings. Fail requires nonempty findings and requests changes. The owner must explicitly assign repairs. If unable to verify, report your work blocker instead."},
		// The wrapper is load-bearing: compose validates branches eagerly, so a
		// composed branch needs a non-nil Invoke even when handle is nil. Passing
		// handle directly panics at construction (tool/work_test.go:162).
		Func[AuditInput]{Spec: passAuditDefinition, Invoke: func(ctx context.Context, c Call, a AuditInput) (Result, error) { return handle(ctx, c, a.domain()) }},
		Func[AuditInput]{Spec: failAuditDefinition, Invoke: func(ctx context.Context, c Call, a AuditInput) (Result, error) {
			return handle(ctx, c, a.domain())
		}},
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
	return Func[work.CancelRequest]{Spec: cancelWorkDefinition, Invoke: handle}
}
func ReassignWork(handle Handler[work.ReassignRequest]) Tool {
	return Func[work.ReassignRequest]{Spec: Definition[work.ReassignRequest]{Bookkeeping: []string{"expected_revision"}, Name: "reassign_work", Description: "Replace the worker on an existing active work item. Supply only work_id, expected_revision, and the required existing assignee. Create a replacement explicitly with create_agent if needed. Uses this work item's revision. Does not create, resume, or stop agents. Old assignment updates are rejected.", Parameters: reassignmentParameters}, Invoke: handle}
}

func ListWork(handle Handler[work.ListQuery]) Tool {
	type first struct {
		Assignee *identity.ActorID `json:"assignee"`
		Kind     *work.Kind        `json:"kind"`
		State    *work.State       `json:"state"`
		Limit    *int              `json:"limit"`
	}
	type next struct {
		Cursor string `json:"cursor"`
		Limit  *int   `json:"limit"`
	}
	return compose(provider.ToolDefinition{Name: "list_work", Description: "Discover tracked work, including closed and cancelled work. Root only. Start with nullable assignee, kind and state filters; continue with cursor and nullable limit only. Results describe a fixed recorded snapshot. Use get_work for current details before mutations. Discovery does not guarantee exactly-once retries."},
		builtin("first_work_page", "", func(ctx context.Context, c Call, q first) (Result, error) {
			return handle(ctx, c, work.ListQuery{Assignee: valueOrZero(q.Assignee), Kind: valueOrZero(q.Kind), State: valueOrZero(q.State), Limit: valueOrZero(q.Limit)})
		}, Nullable("assignee", "all assignees"), Nullable("kind", "all work kinds"), Nullable("state", "all work states"), Nullable("limit", "use the default page size"), Enum("kind", "implementation", "audit", "repair", "research"), Enum("state", "active", "needs_check", "checking", "changes_requested", "accepted", "closed", "cancelled", "delivered"), Minimum("limit", 1), Maximum("limit", 100)),
		builtin("next_work_page", "", func(ctx context.Context, c Call, q next) (Result, error) {
			return handle(ctx, c, work.ListQuery{Cursor: q.Cursor, Limit: valueOrZero(q.Limit)})
		}, Nullable("limit", "use the default page size"), MinLength("cursor", 1), Minimum("limit", 1), Maximum("limit", 100)))
}

func SubmitResearch(h Handler[work.SubmitResearchRequest]) Tool {
	return Func[SubmitResearchInput]{Spec: submitResearchDefinition, Invoke: func(ctx context.Context, c Call, a SubmitResearchInput) (Result, error) { return h(ctx, c, a.domain()) }}
}
