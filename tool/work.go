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
		branches = append(branches, Func[createPlanArgs]{
			Spec: Definition[createPlanArgs]{
				Name:        "create_plan",
				Description: "Create a plan with a title and new steps. Omit all IDs.",
				Parameters:  parameters[createPlanArgs](MinLength("title", 1), MinItems("steps", 1), MinLength("steps[].title", 1)),
			},
			Invoke: func(ctx context.Context, c Call, a createPlanArgs) (Result, error) {
				steps := make([]work.StepEdit, len(a.Steps))
				for i, step := range a.Steps {
					steps[i] = work.StepEdit{Title: &step.Title}
					if step.AcceptanceCriteria != nil {
						steps[i].AcceptanceCriteria = &step.AcceptanceCriteria
					}
				}
				return plan(ctx, c, work.PlanUpdate{Title: &a.Title, Steps: steps})
			},
		}, Func[editPlanArgs]{
			Spec: Definition[editPlanArgs]{
				Name:        "edit_plan",
				Description: "Edit owned structure using plan_id and expected_revision. Existing steps use step_id; new steps require title.",
				Parameters:  parameters[editPlanArgs](MinLength("plan_id", 1), Minimum("expected_revision", 1), MinLength("title", 1), MinLength("steps[].step_id", 1), MinLength("steps[].title", 1), AtLeastOne("steps[]", "step_id", "title"), UniqueItems("order"), UniqueItems("cancel")),
			},
			Invoke: func(ctx context.Context, c Call, a editPlanArgs) (Result, error) {
				return plan(ctx, c, work.PlanUpdate{PlanID: &a.PlanID, ExpectedRevision: &a.ExpectedRevision, Title: a.Title, Steps: a.Steps, Order: a.Order, Cancel: a.Cancel})
			},
		})
	}
	if progress != nil {
		guidance = append(guidance, `Progress: {"work_id":"<work_id>","expected_revision":1,"steps":[{"step_id":"<step_id>","status":"in_progress"}]}. Use work.revision from get_work, not the plan revision or assigned_at_revision. Each successful update returns a new work revision; use that revision for the next mutation, including submit_work. Each step allows step_id, optional status and optional note; never title. Status is pending, in_progress, blocked or ready_for_review. Top-level note and blocker update the work; an empty blocker clears it. Only audits complete work.`)
		branches = append(branches, Func[work.ProgressUpdate]{
			Spec: Definition[work.ProgressUpdate]{
				Name:        "update_progress",
				Description: "Update assigned scoped progress using work_id and expected_revision. Only an audit can complete steps.",
				Parameters:  parameters[work.ProgressUpdate](MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("steps[].step_id", 1), Enum("steps[].status", "pending", "in_progress", "blocked", "ready_for_review")),
			},
			Invoke: progress,
		})
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
	return Func[args]{
		Spec: Definition[args]{
			Name:        "update_work",
			Description: "Report a note or blocker on your assigned work. A blocker reports inability to verify; it is not a verdict.",
			Parameters:  parameters[args](Minimum("expected_revision", 1), MinLength("work_id", 1)),
		},
		Invoke: func(ctx context.Context, c Call, a args) (Result, error) {
			return handle(ctx, c, work.ProgressUpdate{WorkTarget: a.WorkTarget, Note: a.Note, Blocker: a.Blocker})
		},
	}
}

type AssignWorkArgs struct {
	Kind             work.Kind         `json:"kind"`
	Assignee         identity.ActorID  `json:"assignee,omitempty"`
	Task             string            `json:"task,omitempty"`
	Context          string            `json:"context,omitempty"`
	ExpectedOutput   string            `json:"expected_output,omitempty"`
	Scope            *work.Scope       `json:"scope,omitempty"`
	WorkID           work.ID           `json:"work_id,omitempty"`
	ExpectedRevision work.Revision     `json:"expected_revision,omitempty"`
	SubmissionID     work.SubmissionID `json:"submission_id,omitempty"`
}

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
		Func[implementation]{
			Spec: Definition[implementation]{
				Name:       "assign_implementation",
				Parameters: parameters[implementation](Enum("kind", "implementation"), MinLength("task", 1), MinLength("assignee", 1), MinLength("scope.plan_id", 1), MinItems("scope.step_ids", 1), UniqueItems("scope.step_ids"), MinLength("scope.step_ids[]", 1)),
			},
			Invoke: func(ctx context.Context, c Call, a implementation) (Result, error) {
				return handle(ctx, c, AssignWorkArgs{Kind: a.Kind, Assignee: a.Assignee, Task: a.Task, Context: a.Context, ExpectedOutput: a.ExpectedOutput, Scope: a.Scope})
			},
		},
		Func[audit]{
			Spec: Definition[audit]{
				Name:       "assign_audit",
				Parameters: parameters[audit](Enum("kind", "audit"), MinLength("assignee", 1), MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("submission_id", 1)),
			},
			Invoke: func(ctx context.Context, c Call, a audit) (Result, error) {
				return handle(ctx, c, AssignWorkArgs{Kind: a.Kind, Assignee: a.Assignee, WorkID: a.WorkID, ExpectedRevision: a.ExpectedRevision, SubmissionID: a.SubmissionID})
			},
		},
	)
}
func SubmitWork(handle Handler[work.SubmitRequest]) Tool {
	return Func[work.SubmitRequest]{
		Spec: Definition[work.SubmitRequest]{
			Name:        "submit_work",
			Description: "Submit implementation or repairs for audit. All scoped steps must be ready_for_review and your blocker cleared. A text reply does not submit work.",
			Parameters:  parameters[work.SubmitRequest](Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("summary", 1), MinLength("artifacts[].uri", 1)),
		},
		Invoke: handle,
	}
}
func SubmitAudit(handle Handler[work.AuditRequest]) Tool {
	type pass struct {
		work.WorkTarget
		SubmissionID work.SubmissionID `json:"submission_id"`
		Verdict      work.Verdict      `json:"verdict"`
		Summary      string            `json:"summary"`
		Findings     []work.Finding    `json:"findings,omitempty"`
	}
	type fail struct {
		work.WorkTarget
		SubmissionID work.SubmissionID `json:"submission_id"`
		Verdict      work.Verdict      `json:"verdict"`
		Summary      string            `json:"summary"`
		Findings     []work.Finding    `json:"findings"`
	}
	return compose(provider.ToolDefinition{Name: "submit_audit", Description: "Record pass or fail for your assigned submission. Pass permits omitted or empty findings. Fail requires nonempty findings and issues scoped repairs. If unable to verify, report your work blocker instead."},
		Func[pass]{
			Spec: Definition[pass]{
				Name:       "pass_audit",
				Parameters: parameters[pass](Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("submission_id", 1), Enum("verdict", "pass"), MinLength("summary", 1), MaxItems("findings", 0)),
			},
			Invoke: func(ctx context.Context, c Call, a pass) (Result, error) {
				return handle(ctx, c, work.AuditRequest{WorkTarget: a.WorkTarget, SubmissionID: a.SubmissionID, Verdict: a.Verdict, Summary: a.Summary, Findings: a.Findings})
			},
		},
		Func[fail]{
			Spec: Definition[fail]{
				Name:       "fail_audit",
				Parameters: parameters[fail](Minimum("expected_revision", 1), MinLength("work_id", 1), MinLength("submission_id", 1), Enum("verdict", "fail"), MinLength("summary", 1), MinItems("findings", 1), MinLength("findings[].description", 1), MinLength("findings[].required_change", 1), MinLength("findings[].verification", 1)),
			},
			Invoke: func(ctx context.Context, c Call, a fail) (Result, error) {
				return handle(ctx, c, work.AuditRequest{WorkTarget: a.WorkTarget, SubmissionID: a.SubmissionID, Verdict: a.Verdict, Summary: a.Summary, Findings: a.Findings})
			},
		},
	)
}
func GetWork(handle func(context.Context, Call, work.ID) (Result, error)) Tool {
	type args struct {
		ID work.ID `json:"work_id"`
	}
	return Func[args]{
		Spec: Definition[args]{
			Name:        "get_work",
			Description: "Read current work, scoped steps, submission and repair findings. Use work.revision as expected_revision for work mutations; assigned_at_revision is only the assignment binding. Returned steps are snapshots, not update patches.",
			Parameters:  parameters[args](MinLength("work_id", 1)),
		},
		Invoke: func(ctx context.Context, c Call, a args) (Result, error) { return handle(ctx, c, a.ID) },
	}
}
func GetPlan(handle func(context.Context, Call, work.PlanID) (Result, error)) Tool {
	type args struct {
		ID work.PlanID `json:"plan_id"`
	}
	return Func[args]{
		Spec: Definition[args]{
			Name:        "get_plan",
			Description: "Read a plan; delegates receive their authorized subset. Use revision as expected_revision for structural plan edits only. Work progress uses the separate work.revision from get_work. Returned steps include read-only status; do not copy whole snapshots into structural edits.",
			Parameters:  parameters[args](MinLength("plan_id", 1)),
		},
		Invoke: func(ctx context.Context, c Call, a args) (Result, error) { return handle(ctx, c, a.ID) },
	}
}
func GetAudit(handle func(context.Context, Call, work.AuditID) (Result, error)) Tool {
	type args struct {
		ID work.AuditID `json:"audit_id"`
	}
	return Func[args]{
		Spec: Definition[args]{
			Name:        "get_audit",
			Description: "Read the recorded verdict, summary and findings for an audit_id from a work event.",
			Parameters:  parameters[args](MinLength("audit_id", 1)),
		},
		Invoke: func(ctx context.Context, c Call, a args) (Result, error) { return handle(ctx, c, a.ID) },
	}
}
func CancelWork(handle Handler[work.CancelRequest]) Tool {
	return Func[work.CancelRequest]{
		Spec: Definition[work.CancelRequest]{
			Name:        "cancel_work",
			Description: "Owner cancels work. Cancelling implementation or repair ends that cycle; cancelling audit returns its submission for another review.",
			Parameters:  parameters[work.CancelRequest](MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("reason", 1)),
		},
		Invoke: handle,
	}
}
func ReassignWork(handle Handler[work.ReassignRequest]) Tool {
	type args struct {
		work.WorkTarget
		Assignee identity.ActorID `json:"assignee,omitempty"`
	}
	return Func[args]{
		Spec: Definition[args]{
			Name:        "reassign_work",
			Description: "Owner reassigns active work. Omit assignee to provision a replacement. Old assignment updates are rejected.",
			Parameters:  parameters[args](MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("assignee", 1)),
		},
		Invoke: func(ctx context.Context, c Call, a args) (Result, error) {
			return handle(ctx, c, work.ReassignRequest{WorkTarget: a.WorkTarget, Assignee: a.Assignee})
		},
	}
}
