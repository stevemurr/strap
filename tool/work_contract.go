package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/identity"
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

// AssignImplementationArgs starts new implementation work for an existing agent.
type AssignImplementationArgs struct {
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        *string          `json:"context"`
	ExpectedOutput *string          `json:"expected_output"`
	Scope          *work.Scope      `json:"scope"`
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

// AssignResearchArgs starts a bounded investigation for an existing researcher.
type AssignResearchArgs struct {
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        *string          `json:"context"`
	ExpectedOutput *string          `json:"expected_output"`
}

var implementationContract = assignmentOperation[AssignImplementationArgs]{
	definition: Definition[AssignImplementationArgs]{
		Name:        "assign_implementation",
		Description: "Assign new implementation work to an existing implementor. Supply a task and nullable context, expected_output, and scope containing plan_id and step_ids. Creates tracked work; the receipt is registration, not completion. Use reassign_work to transfer existing work.",
		Parameters: parameters[AssignImplementationArgs](Nullable("context", "no additional context"), Nullable("expected_output", "no additional output requirements"), Nullable("scope", "unscoped work"),
			MinLength("assignee", 1), MinLength("task", 1),
			MinLength("scope.plan_id", 1), MinItems("scope.step_ids", 1),
			MinLength("scope.step_ids[]", 1)),
	},
	normalize: func(a AssignImplementationArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: work.Implementation, Assignee: a.Assignee, Task: a.Task, Context: valueOrZero(a.Context), ExpectedOutput: valueOrZero(a.ExpectedOutput), Scope: a.Scope}
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
		Description: "Assign repairs for a failed audit to an existing implementor. Use the original implementation work_id, its current revision as expected_revision, and the failing verdict's audit_id. Task, findings, and scope are derived from the original work and audit. After repair submission, assign an independent audit of the original work's latest submission.",
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

var researchContract = assignmentOperation[AssignResearchArgs]{
	definition: Definition[AssignResearchArgs]{
		Name:        "assign_research",
		Description: "Assign a bounded investigation to an existing researcher. Supply the question as task and nullable context and expected_output. Include relevant plan information in context. Creates tracked research work; the receipt is registration, not findings or completion.",
		Parameters:  parameters[AssignResearchArgs](Nullable("context", "no additional context"), Nullable("expected_output", "no additional output requirements"), MinLength("assignee", 1), MinLength("task", 1)),
	},
	normalize: func(a AssignResearchArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: work.Research, Assignee: a.Assignee, Task: a.Task, Context: valueOrZero(a.Context), ExpectedOutput: valueOrZero(a.ExpectedOutput)}
	},
}

func assignmentContracts() []assignmentContract {
	return []assignmentContract{implementationContract, auditContract, repairContract, researchContract}
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

var reassignmentParameters = parameters[work.ReassignRequest](MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("assignee", 1))

func DecodeReassignment(raw json.RawMessage) (work.ReassignRequest, error) {
	return reassignmentParameters.Decode(raw)
}
