package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
	"strings"
)

// A branch couples its schema, pure decoding, and normalization. HTTP never
// executes a tool callback to decode arguments.
type assignmentContract interface {
	tool(Handler[work.AssignmentRequest]) Tool
	decode(json.RawMessage) (work.AssignmentRequest, error)
}
type assignmentBranch[A any] struct {
	name       string
	parameters Parameters[A]
	normalize  func(A) work.AssignmentRequest
}

func (b assignmentBranch[A]) tool(h Handler[work.AssignmentRequest]) Tool {
	return Func[A]{Spec: Definition[A]{Name: b.name, Parameters: b.parameters}, Invoke: func(ctx context.Context, c Call, a A) (Result, error) {
		r := b.normalize(a)
		if err := r.Validate(); err != nil {
			return Result{}, err
		}
		return h(ctx, c, r)
	}}
}
func (b assignmentBranch[A]) decode(raw json.RawMessage) (work.AssignmentRequest, error) {
	a, err := b.parameters.Decode(raw)
	if err != nil {
		return work.AssignmentRequest{}, err
	}
	return b.normalize(a), nil
}

type implementationArgs struct {
	Kind           work.Kind        `json:"kind"`
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
	Scope          *work.Scope      `json:"scope,omitempty"`
}
type auditArgs struct {
	Kind     work.Kind        `json:"kind"`
	Assignee identity.ActorID `json:"assignee"`
	work.WorkTarget
	SubmissionID work.SubmissionID `json:"submission_id"`
}
type repairArgs struct {
	Kind     work.Kind        `json:"kind"`
	Assignee identity.ActorID `json:"assignee"`
	work.WorkTarget
	AuditID work.AuditID `json:"audit_id"`
}

var implementationContract = assignmentBranch[implementationArgs]{"assign_implementation", parameters[implementationArgs](
	Description("task", "Implementation only. Omit task for audit and repair; their task is derived from the original work."),
	Description("context", "Implementation only; omit for audit and repair."),
	Description("expected_output", "Implementation only; omit for audit and repair."),
	Description("scope", "Implementation only; audit and repair scopes are derived and must be omitted."),
	Enum("kind", "implementation"), MinLength("assignee", 1), MinLength("task", 1), MinLength("scope.plan_id", 1), MinItems("scope.step_ids", 1), UniqueItems("scope.step_ids"), MinLength("scope.step_ids[]", 1)),
	func(a implementationArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: a.Kind, Assignee: a.Assignee, Task: a.Task, Context: a.Context, ExpectedOutput: a.ExpectedOutput, Scope: a.Scope}
	},
}
var auditContract = assignmentBranch[auditArgs]{"assign_audit", parameters[auditArgs](
	Enum("kind", "audit"), MinLength("assignee", 1), MinLength("work_id", 1), Minimum("expected_revision", 1), Description("expected_revision", "Audit or repair only: use the original work revision. Omit for new implementation assignments, including reuse of an existing agent."), MinLength("submission_id", 1)),
	func(a auditArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: a.Kind, Assignee: a.Assignee, WorkID: a.ID, ExpectedRevision: a.ExpectedRevision, SubmissionID: a.SubmissionID}
	},
}
var repairContract = assignmentBranch[repairArgs]{"assign_repair", parameters[repairArgs](
	Enum("kind", "repair"), MinLength("assignee", 1), MinLength("work_id", 1), Minimum("expected_revision", 1), Description("expected_revision", "Audit or repair only: use the original work revision. Omit for new implementation assignments, including reuse of an existing agent."), MinLength("audit_id", 1)),
	func(a repairArgs) work.AssignmentRequest {
		return work.AssignmentRequest{Kind: a.Kind, Assignee: a.Assignee, WorkID: a.ID, ExpectedRevision: a.ExpectedRevision, AuditID: a.AuditID}
	},
}

type researchArgs struct {
	Kind           work.Kind        `json:"kind"`
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
}

var researchContract = assignmentBranch[researchArgs]{"assign_research", parameters[researchArgs](Enum("kind", "research"), MinLength("assignee", 1), MinLength("task", 1)), func(a researchArgs) work.AssignmentRequest {
	return work.AssignmentRequest{Kind: a.Kind, Assignee: a.Assignee, Task: a.Task, Context: a.Context, ExpectedOutput: a.ExpectedOutput}
}}

func assignmentContracts() []assignmentContract {
	return []assignmentContract{implementationContract, auditContract, repairContract, researchContract}
}
func DecodeAssignment(raw json.RawMessage) (work.AssignmentRequest, error) {
	var selected work.AssignmentRequest
	matches := 0
	failures := []string{}
	for _, b := range assignmentContracts() {
		a, err := b.decode(raw)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		matches++
		selected = a
	}
	if matches != 1 {
		return work.AssignmentRequest{}, fmt.Errorf("%w: assignment must match exactly one operation: %s", work.ErrInvalid, strings.Join(failures, "; "))
	}
	return selected, selected.Validate()
}

var reassignmentParameters = parameters[work.ReassignRequest](MinLength("work_id", 1), Minimum("expected_revision", 1), MinLength("assignee", 1))

func DecodeReassignment(raw json.RawMessage) (work.ReassignRequest, error) {
	return reassignmentParameters.Decode(raw)
}
