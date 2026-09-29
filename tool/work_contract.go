package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// Each operation owns one contract for its schema, tool calls, and HTTP decoding.
// The operation name selects the command; its arguments contain no discriminator.
type assignmentContract interface {
	name() string
	tool(Handler[work.AssignmentRequest]) Tool
	decode(json.RawMessage) (work.AssignmentRequest, error)
}

type assignmentOperation[A any] struct {
	definition Definition[A]
	normalize  func(A) work.AssignmentRequest
}

func (o assignmentOperation[A]) name() string { return o.definition.Name }

func (o assignmentOperation[A]) command(a A) (work.AssignmentRequest, error) {
	r := o.normalize(a)
	return r, r.Validate()
}

func (o assignmentOperation[A]) tool(h Handler[work.AssignmentRequest]) Tool {
	return Func[A]{
		Spec: o.definition,
		Invoke: func(ctx context.Context, c Call, a A) (Result, error) {
			r, err := o.command(a)
			if err != nil {
				return Result{}, err
			}
			return h(ctx, c, r)
		},
	}
}

func (o assignmentOperation[A]) decode(raw json.RawMessage) (work.AssignmentRequest, error) {
	a, err := o.definition.Parameters.Decode(raw)
	if err != nil {
		return work.AssignmentRequest{}, err
	}
	return o.command(a)
}

// AssignTaskArgs starts new work. Kind names the work and the role that does
// it. Implementation, review, research and experiments take the same fields,
// so one command carries them with no conditional requirements; audits and
// repairs bind an existing submission and keep their own commands.
type AssignTaskArgs struct {
	Kind           work.Kind         `json:"kind"`
	Assignee       *identity.ActorID `json:"assignee"`
	Task           string            `json:"task"`
	Context        *string           `json:"context"`
	ExpectedOutput *string           `json:"expected_output"`
	Scope          *work.Scope       `json:"scope"`
}

// AssignAuditArgs binds an independent audit to an original work and submission.
type AssignAuditArgs struct {
	Assignee identity.ActorID `json:"assignee"`
	work.WorkTarget
	SubmissionID work.SubmissionID `json:"submission_id"`
}

// AssignRepairArgs binds repairs to the failed audit of an original work.
type AssignRepairArgs struct {
	Assignee identity.ActorID `json:"assignee"`
	work.WorkTarget
	AuditID work.AuditID `json:"audit_id"`
}

var taskContract = assignmentOperation[AssignTaskArgs]{
	definition: Definition[AssignTaskArgs]{
		Name:        "assign_task",
		Description: "Assign new work to a worker. kind names the work and the role that does it: implementation, an implementor changes files; review, a reviewer reads files anywhere on this machine and delivers a brief; web_research, a web_researcher answers a bounded question from web searches and pages and delivers a brief; deep_research, a deep_researcher runs a long multi-source deep research investigation, only when the user explicitly asks for deep research; experiment, an experimenter measures how the code actually behaves: performance, reproducing a bug, the effect of a setting; it forms hypotheses, measures them in its own copy of the workspace, and delivers a conclusion with the method that reproduces the measurements. Set assignee to an idle agent_id of that role to reuse it, or null to create a new one. Supply the work as task, nullable context and expected_output, and a nullable scope naming the plan_id and step_ids of the phase: a delivered brief or conclusion completes those steps, and implementation completes them when its audit passes. Creates tracked work; the receipt is registration, not results or completion.",
		Parameters: parameters[AssignTaskArgs](Enum("kind", string(work.Implementation), string(work.Review), string(work.WebResearch), string(work.DeepResearch), string(work.Experiment)),
			Nullable("assignee", "create a new agent of the kind's role"), Nullable("context", "no additional context"), Nullable("expected_output", "no additional output requirements"), Nullable("scope", "work outside the plan"),
			MinLength("assignee", 1), MinLength("task", 1),
			MinLength("scope.plan_id", 1), MinItems("scope.step_ids", 1),
			MinLength("scope.step_ids[]", 1)),
	},
	normalize: func(a AssignTaskArgs) work.AssignmentRequest {
		// A null assignee staffs the work: naming the kind's role creates its agent.
		assignee := valueOrZero(a.Assignee)
		if assignee == "" {
			assignee = identity.ActorID(roster.RoleFor(a.Kind))
		}
		return work.AssignmentRequest{Kind: a.Kind, Assignee: assignee, Task: a.Task, Context: valueOrZero(a.Context), ExpectedOutput: valueOrZero(a.ExpectedOutput), Scope: a.Scope}
	},
}

var auditContract = assignmentOperation[AssignAuditArgs]{
	definition: Definition[AssignAuditArgs]{
		Name:        "assign_audit",
		Description: "Assign an independent audit of a specific submission to an existing auditor. Use the original implementation work_id, its current revision as expected_revision, and latest_submission_id as submission_id. The auditor must not have implemented or repaired this submission chain. Task and scope are derived from the original work.",
		Bookkeeping: []string{"expected_revision"},
		Parameters: parameters[AssignAuditArgs](
			MinLength("assignee", 1), MinLength("work_id", 1), Minimum("expected_revision", 1),
			Description("expected_revision", "Use the current revision of the original implementation work."),
			MinLength("submission_id", 1)),
	},
	normalize: func(a AssignAuditArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: work.AuditWork, Assignee: a.Assignee, WorkID: a.ID, ExpectedRevision: a.ExpectedRevision, SubmissionID: a.SubmissionID}
	},
}

var repairContract = assignmentOperation[AssignRepairArgs]{
	definition: Definition[AssignRepairArgs]{
		Name:        "assign_repair",
		Description: "Assign repairs for a failed audit to an implementor: name an idle implementor's agent_id as assignee, or implementor to create a new one. Use the original implementation work_id, its current revision as expected_revision, and the failing verdict's audit_id. Task, findings, and scope are derived from the original work and audit. After repair submission, assign an independent audit of the original work's latest submission.",
		Bookkeeping: []string{"expected_revision"},
		Parameters: parameters[AssignRepairArgs](
			MinLength("assignee", 1), MinLength("work_id", 1), Minimum("expected_revision", 1),
			Description("expected_revision", "Use the current revision of the original implementation work."),
			MinLength("audit_id", 1)),
	},
	normalize: func(a AssignRepairArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: work.Repair, Assignee: a.Assignee, WorkID: a.ID, ExpectedRevision: a.ExpectedRevision, AuditID: a.AuditID}
	},
}

func assignmentContracts() []assignmentContract {
	return []assignmentContract{taskContract, auditContract, repairContract}
}

// AssignmentTools exposes each assignment operation using its complete contract.
func AssignmentTools(h Handler[work.AssignmentRequest]) []Tool {
	var tools []Tool
	for _, operation := range assignmentContracts() {
		tools = append(tools, operation.tool(h))
	}
	return tools
}

// DecodeAssignment validates the named operation without executing a tool handler.
func DecodeAssignment(name string, raw json.RawMessage) (work.AssignmentRequest, error) {
	for _, operation := range assignmentContracts() {
		if operation.name() == name {
			return operation.decode(raw)
		}
	}
	return work.AssignmentRequest{}, fmt.Errorf("%w: unknown assignment operation %q", work.ErrInvalid, name)
}
