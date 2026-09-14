package work

import "github.com/stevemurr/strap/identity"

// AssignmentRequest is a normalized command. Wire adapters validate field presence
// before constructing it; all assignments require an existing assignee.
type AssignmentRequest struct {
	Kind             Kind             `json:"kind"`
	Assignee         identity.ActorID `json:"assignee"`
	Task             string           `json:"task,omitempty"`
	Context          string           `json:"context,omitempty"`
	ExpectedOutput   string           `json:"expected_output,omitempty"`
	Scope            *Scope           `json:"scope,omitempty"`
	WorkID           ID               `json:"work_id,omitempty"`
	ExpectedRevision Revision         `json:"expected_revision,omitempty"`
	SubmissionID     SubmissionID     `json:"submission_id,omitempty"`
	AuditID          AuditID          `json:"audit_id,omitempty"`
}

// Inspection contains authorized work details assembled by the application.
// Fields may come from separate ledger reads; this is not a global checkpoint.
type Inspection struct {
	Work       Work        `json:"work"`
	Steps      []Step      `json:"steps,omitempty"`
	Submission *Submission `json:"submission,omitempty"`
	Audit      *Audit      `json:"audit,omitempty"`
}

// Validate checks semantics shared by typed hosts and wire adapters.
func (r AssignmentRequest) Validate() error {
	if blank(string(r.Assignee)) {
		return invalid("assignee is required; create_agent first or select an existing eligible agent")
	}
	switch r.Kind {
	case Implementation:
		if blank(r.Task) || r.WorkID != "" || r.ExpectedRevision != 0 || r.SubmissionID != "" || r.AuditID != "" {
			return invalid("implementation requires task and cannot select a submission or audit")
		}
	case Research:
		if blank(r.Task) || r.Scope != nil || r.WorkID != "" || r.ExpectedRevision != 0 || r.SubmissionID != "" || r.AuditID != "" {
			return invalid("research requires task and forbids scope, submission and audit selectors")
		}
	case AuditWork, Repair:
		if blank(string(r.WorkID)) || r.ExpectedRevision == 0 || r.Scope != nil || r.Task != "" || r.Context != "" || r.ExpectedOutput != "" {
			return invalid("audit and repair require original work_id and expected_revision; task and scope are derived")
		}
		if r.Kind == AuditWork && (blank(string(r.SubmissionID)) || r.AuditID != "") {
			return invalid("audit requires submission_id and forbids audit_id")
		}
		if r.Kind == Repair && (blank(string(r.AuditID)) || r.SubmissionID != "") {
			return invalid("repair requires audit_id and forbids submission_id")
		}
	default:
		return invalid("kind must be implementation, audit, repair, or research")
	}
	return nil
}

type ListQuery struct {
	Assignee identity.ActorID `json:"assignee,omitempty"`
	Kind     Kind             `json:"kind,omitempty"`
	State    State            `json:"state,omitempty"`
	Cursor   string           `json:"cursor,omitempty"`
	Limit    int              `json:"limit,omitempty"`
}

type Summary struct {
	ID                 ID               `json:"work_id"`
	Kind               Kind             `json:"kind"`
	State              State            `json:"state"`
	Revision           Revision         `json:"revision"`
	Owner              identity.ActorID `json:"owner"`
	Assignee           identity.ActorID `json:"assignee"`
	ParentID           ID               `json:"parent_id,omitempty"`
	RequestedByAuditID AuditID          `json:"requested_by_audit_id,omitempty"`
	LatestSubmissionID SubmissionID     `json:"latest_submission_id,omitempty"`
	TaskPreview        string           `json:"task_preview"`
}

type ListPage struct {
	Items      []Summary `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
