package work

import "github.com/stevemurr/strap/identity"

// AssignmentRequest selects implementation or audit work at the application
// boundary. An omitted assignee requests provisioning; Store never creates agents.
type AssignmentRequest struct {
	Kind             Kind             `json:"kind"`
	Assignee         identity.ActorID `json:"assignee,omitempty"`
	Task             string           `json:"task,omitempty"`
	Context          string           `json:"context,omitempty"`
	ExpectedOutput   string           `json:"expected_output,omitempty"`
	Scope            *Scope           `json:"scope,omitempty"`
	WorkID           ID               `json:"work_id,omitempty"`
	ExpectedRevision Revision         `json:"expected_revision,omitempty"`
	SubmissionID     SubmissionID     `json:"submission_id,omitempty"`
}

// Inspection contains authorized work details assembled by the application.
// Fields may come from separate ledger reads; this is not a global checkpoint.
type Inspection struct {
	Work       Work        `json:"work"`
	Steps      []Step      `json:"steps,omitempty"`
	Submission *Submission `json:"submission,omitempty"`
	Audit      *Audit      `json:"audit,omitempty"`
}
