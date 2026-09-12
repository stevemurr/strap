// Package work owns shared plans and the implementation, audit, and repair cycle.
// Returned records are snapshots; only Store operations confer or change authority.
package work

import (
	"errors"
	"slices"

	"github.com/stevemurr/strap/identity"
)

type ID string
type PlanID string
type StepID string
type SubmissionID string
type AuditID string
type EventID string
type Revision uint64
type Kind string
type State string
type StepStatus string
type Verdict string
type EventKind string

const (
	Implementation   Kind       = "implementation"
	AuditWork        Kind       = "audit"
	Repair           Kind       = "repair"
	Active           State      = "active"
	NeedsCheck       State      = "needs_check"
	Checking         State      = "checking"
	ChangesRequested State      = "changes_requested"
	Accepted         State      = "accepted"
	Closed           State      = "closed"
	Cancelled        State      = "cancelled"
	Pending          StepStatus = "pending"
	InProgress       StepStatus = "in_progress"
	Blocked          StepStatus = "blocked"
	ReadyForReview   StepStatus = "ready_for_review"
	Completed        StepStatus = "completed"
	CancelledStep    StepStatus = "cancelled"
	Pass             Verdict    = "pass"
	Fail             Verdict    = "fail"
	PlanChanged      EventKind  = "plan_changed"
	WorkAssigned     EventKind  = "work_assigned"
	ProgressChanged  EventKind  = "progress_changed"
	ReviewRequested  EventKind  = "review_requested"
	AuditCompleted   EventKind  = "audit_completed"
	WorkReassigned   EventKind  = "work_reassigned"
	WorkCancelled    EventKind  = "work_cancelled"
)

var (
	ErrNotFound  = errors.New("work: not found")
	ErrForbidden = errors.New("work: forbidden")
	ErrConflict  = errors.New("work: stale revision")
	ErrInvalid   = errors.New("work: invalid input")
	ErrState     = errors.New("work: invalid transition")
	ErrReserved  = errors.New("work: step reserved")
)

type Plan struct {
	ID       PlanID           `json:"plan_id"`
	Owner    identity.ActorID `json:"owner"`
	Revision Revision         `json:"revision"`
	Title    string           `json:"title"`
	Steps    []Step           `json:"steps"`
}
type Step struct {
	ID                 StepID     `json:"step_id"`
	Title              string     `json:"title"`
	AcceptanceCriteria []string   `json:"acceptance_criteria,omitempty"`
	Status             StepStatus `json:"status"`
	Note               string     `json:"note,omitempty"`
}
type Scope struct {
	PlanID  PlanID   `json:"plan_id"`
	StepIDs []StepID `json:"step_ids"`
}
type Work struct {
	ID                  ID               `json:"work_id"`
	Kind                Kind             `json:"kind"`
	State               State            `json:"state"`
	Revision            Revision         `json:"revision"`
	Owner               identity.ActorID `json:"owner"`
	RequestedBy         identity.ActorID `json:"requested_by"`
	Assignee            identity.ActorID `json:"assignee"`
	AssignedAtRevision  Revision         `json:"assigned_at_revision"`
	Task                string           `json:"task"`
	Context             string           `json:"context,omitempty"`
	ExpectedOutput      string           `json:"expected_output,omitempty"`
	Scope               *Scope           `json:"scope,omitempty"`
	Note                string           `json:"note,omitempty"`
	Blocker             string           `json:"blocker,omitempty"`
	ParentID            ID               `json:"parent_id,omitempty"`
	SubjectSubmissionID SubmissionID     `json:"subject_submission_id,omitempty"`
	RequestedByAuditID  AuditID          `json:"requested_by_audit_id,omitempty"`
	LatestSubmissionID  SubmissionID     `json:"latest_submission_id,omitempty"`
}
type WorkTarget struct {
	ID               ID       `json:"work_id"`
	ExpectedRevision Revision `json:"expected_revision"`
}
type PlanUpdate struct {
	PlanID           *PlanID    `json:"plan_id,omitempty"`
	ExpectedRevision *Revision  `json:"expected_revision,omitempty"`
	Title            *string    `json:"title,omitempty"`
	Steps            []StepEdit `json:"steps,omitempty"`
	Order            []StepID   `json:"order,omitempty"`
	Cancel           []StepID   `json:"cancel,omitempty"`
}
type StepEdit struct {
	ID                 *StepID   `json:"step_id,omitempty"`
	Title              *string   `json:"title,omitempty"`
	AcceptanceCriteria *[]string `json:"acceptance_criteria,omitempty"`
}
type AssignRequest struct {
	Assignee       identity.ActorID `json:"assignee,omitempty"`
	Scope          *Scope           `json:"scope,omitempty"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
}
type ProgressUpdate struct {
	WorkTarget
	Note    *string        `json:"note,omitempty"`
	Blocker *string        `json:"blocker,omitempty"`
	Steps   []StepProgress `json:"steps,omitempty"`
}
type StepProgress struct {
	ID     StepID      `json:"step_id"`
	Status *StepStatus `json:"status,omitempty"`
	Note   *string     `json:"note,omitempty"`
}
type ArtifactRef struct {
	URI      string `json:"uri"`
	Revision string `json:"revision,omitempty"`
}
type SubmitRequest struct {
	WorkTarget
	Summary   string        `json:"summary"`
	Evidence  []string      `json:"evidence,omitempty"`
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`
}
type AssignAuditRequest struct {
	WorkTarget
	SubmissionID SubmissionID     `json:"submission_id"`
	Auditor      identity.ActorID `json:"auditor"`
}
type ReassignRequest struct {
	WorkTarget
	// Applications may provision a replacement when omitted. Store.Reassign
	// still requires a resolved assignee.
	Assignee identity.ActorID `json:"assignee,omitempty"`
}
type CancelRequest struct {
	WorkTarget
	Reason string `json:"reason"`
}
type Submission struct {
	ID             SubmissionID     `json:"submission_id"`
	WorkID         ID               `json:"work_id"`
	SubmittedVia   ID               `json:"submitted_via"`
	SubmittedBy    identity.ActorID `json:"submitted_by"`
	Supersedes     SubmissionID     `json:"supersedes,omitempty"`
	Task           string           `json:"task"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
	Steps          []Step           `json:"steps,omitempty"`
	Summary        string           `json:"summary"`
	Evidence       []string         `json:"evidence,omitempty"`
	Artifacts      []ArtifactRef    `json:"artifacts,omitempty"`
}
type Finding struct {
	StepIDs        []StepID `json:"step_ids,omitempty"`
	Description    string   `json:"description"`
	RequiredChange string   `json:"required_change"`
	Verification   string   `json:"verification"`
}
type AuditRequest struct {
	WorkTarget
	SubmissionID SubmissionID `json:"submission_id"`
	Verdict      Verdict      `json:"verdict"`
	Summary      string       `json:"summary"`
	Findings     []Finding    `json:"findings,omitempty"`
}
type Audit struct {
	ID           AuditID          `json:"audit_id"`
	WorkID       ID               `json:"work_id"`
	SubmissionID SubmissionID     `json:"submission_id"`
	ReviewedBy   identity.ActorID `json:"reviewed_by"`
	Verdict      Verdict          `json:"verdict"`
	Summary      string           `json:"summary"`
	Findings     []Finding        `json:"findings,omitempty"`
	RepairWorkID ID               `json:"repair_work_id,omitempty"`
}
type Event struct {
	Change       *Change          `json:"change,omitempty"`
	ID           EventID          `json:"event_id"`
	Kind         EventKind        `json:"kind"`
	Actor        identity.ActorID `json:"actor"`
	Work         Work             `json:"work"`
	Plan         *Plan            `json:"plan,omitempty"`
	Steps        []Step           `json:"steps,omitempty"`
	SubmissionID SubmissionID     `json:"submission_id,omitempty"`
	AuditID      AuditID          `json:"audit_id,omitempty"`
	Actionable   bool             `json:"actionable"`
}

func cloneSteps(v []Step) []Step {
	v = slices.Clone(v)
	for i := range v {
		v[i].AcceptanceCriteria = slices.Clone(v[i].AcceptanceCriteria)
	}
	return v
}
func (p Plan) Clone() Plan { p.Steps = cloneSteps(p.Steps); return p }
func (w Work) Clone() Work {
	if w.Scope != nil {
		scope := *w.Scope
		scope.StepIDs = slices.Clone(scope.StepIDs)
		w.Scope = &scope
	}
	return w
}
func (s Submission) Clone() Submission {
	s.Steps = cloneSteps(s.Steps)
	s.Evidence = slices.Clone(s.Evidence)
	s.Artifacts = slices.Clone(s.Artifacts)
	return s
}
func (a Audit) Clone() Audit {
	a.Findings = slices.Clone(a.Findings)
	for i := range a.Findings {
		a.Findings[i].StepIDs = slices.Clone(a.Findings[i].StepIDs)
	}
	return a
}
func (e Event) Clone() Event {
	if e.Change != nil {
		v := e.Change.Clone()
		e.Change = &v
	}
	e.Work = e.Work.Clone()
	e.Steps = cloneSteps(e.Steps)
	if e.Plan != nil {
		p := e.Plan.Clone()
		e.Plan = &p
	}
	return e
}
