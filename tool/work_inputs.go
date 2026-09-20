package tool

import "github.com/stevemurr/strap/work"

// Tool input types are distinct from persisted work records. Every field is
// serialized; nullable values retain their identity until domain conversion.

type ReportWorkProgressInput struct {
	WorkID   work.ID                     `json:"work_id"`
	Position *WorkPositionInput          `json:"position"`
	Findings []ProgressFindingDraftInput `json:"findings"`
	Steps    []StepProgressInput         `json:"steps"`
}

type WorkPositionInput struct {
	Objective    string                    `json:"objective"`
	Activity     *string                   `json:"activity"`
	Note         *string                   `json:"note"`
	NextStep     *string                   `json:"next_step"`
	Uncertainty  *string                   `json:"uncertainty"`
	Blocker      *string                   `json:"blocker"`
	DecisionNeed *string                   `json:"decision_need"`
	Dependencies []ProgressDependencyInput `json:"dependencies"`
}

type ProgressDependencyInput struct {
	Need                    string   `json:"need"`
	WorkID                  *work.ID `json:"work_id"`
	PreventsFurtherProgress *bool    `json:"prevents_further_progress"`
}

type ProgressFindingDraftInput struct {
	Claim      string                  `json:"claim"`
	Basis      work.FindingBasis       `json:"basis"`
	Evidence   []EvidenceRefInput      `json:"evidence"`
	Limitation *string                 `json:"limitation"`
	Supersedes *work.ProgressFindingID `json:"supersedes"`
}

type EvidenceRefInput struct {
	URI      string  `json:"uri"`
	Revision *string `json:"revision"`
	Locator  *string `json:"locator"`
	Detail   *string `json:"detail"`
}

type StepProgressInput struct {
	ID     work.StepID      `json:"step_id"`
	Status *work.StepStatus `json:"status"`
	Note   *string          `json:"note"`
}

type SubmitInput struct {
	work.WorkTarget
	Summary   string             `json:"summary"`
	Evidence  []string           `json:"evidence"`
	Artifacts []ArtifactRefInput `json:"artifacts"`
}

type ArtifactRefInput struct {
	URI      string  `json:"uri"`
	Revision *string `json:"revision"`
}

type AuditInput struct {
	work.WorkTarget
	SubmissionID work.SubmissionID `json:"submission_id"`
	Verdict      work.Verdict      `json:"verdict"`
	Summary      string            `json:"summary"`
	Findings     []FindingInput    `json:"findings"`
}

type FindingInput struct {
	StepIDs        []work.StepID `json:"step_ids"`
	Description    string        `json:"description"`
	RequiredChange string        `json:"required_change"`
	Verification   string        `json:"verification"`
}

type SubmitResearchInput struct {
	work.WorkTarget
	Summary        string                   `json:"summary"`
	FindingIDs     []work.ProgressFindingID `json:"finding_ids"`
	OpenQuestions  []string                 `json:"open_questions"`
	Recommendation *string                  `json:"recommendation"`
	ProposedSteps  []ProposedStepInput      `json:"proposed_steps"`
}

type ProposedStepInput struct {
	Title              string   `json:"title"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

func (a ReportWorkProgressInput) domain() work.ReportWorkProgressRequest {
	return work.ReportWorkProgressRequest{WorkID: a.WorkID, Position: mapPointer(a.Position, WorkPositionInput.domain), Findings: mapInputs(a.Findings, ProgressFindingDraftInput.domain), Steps: mapInputs(a.Steps, StepProgressInput.domain)}
}

func (a WorkPositionInput) domain() work.WorkPosition {
	return work.WorkPosition{Objective: a.Objective, Activity: valueOrZero(a.Activity), Note: valueOrZero(a.Note), NextStep: valueOrZero(a.NextStep), Uncertainty: valueOrZero(a.Uncertainty), Blocker: valueOrZero(a.Blocker), DecisionNeed: valueOrZero(a.DecisionNeed), Dependencies: mapInputs(a.Dependencies, ProgressDependencyInput.domain)}
}

func (a ProgressDependencyInput) domain() work.ProgressDependency {
	return work.ProgressDependency{Need: a.Need, WorkID: valueOrZero(a.WorkID), PreventsFurtherProgress: valueOrZero(a.PreventsFurtherProgress)}
}

func (a ProgressFindingDraftInput) domain() work.ProgressFindingDraft {
	return work.ProgressFindingDraft{Claim: a.Claim, Basis: a.Basis, Evidence: mapInputs(a.Evidence, EvidenceRefInput.domain), Limitation: valueOrZero(a.Limitation), Supersedes: valueOrZero(a.Supersedes)}
}

func (a EvidenceRefInput) domain() work.EvidenceRef {
	return work.EvidenceRef{URI: a.URI, Revision: valueOrZero(a.Revision), Locator: valueOrZero(a.Locator), Detail: valueOrZero(a.Detail)}
}

func (a StepProgressInput) domain() work.StepProgress {
	return work.StepProgress{ID: a.ID, Status: a.Status, Note: a.Note}
}

func (a SubmitInput) domain() work.SubmitRequest {
	return work.SubmitRequest{WorkTarget: a.WorkTarget, Summary: a.Summary, Evidence: a.Evidence, Artifacts: mapInputs(a.Artifacts, ArtifactRefInput.domain)}
}

func (a ArtifactRefInput) domain() work.ArtifactRef {
	return work.ArtifactRef{URI: a.URI, Revision: valueOrZero(a.Revision)}
}

func (a AuditInput) domain() work.AuditRequest {
	return work.AuditRequest{WorkTarget: a.WorkTarget, SubmissionID: a.SubmissionID, Verdict: a.Verdict, Summary: a.Summary, Findings: mapInputs(a.Findings, FindingInput.domain)}
}

func (a FindingInput) domain() work.Finding {
	return work.Finding{StepIDs: a.StepIDs, Description: a.Description, RequiredChange: a.RequiredChange, Verification: a.Verification}
}

func (a SubmitResearchInput) domain() work.SubmitResearchRequest {
	return work.SubmitResearchRequest{WorkTarget: a.WorkTarget, Summary: a.Summary, FindingIDs: a.FindingIDs, OpenQuestions: a.OpenQuestions, Recommendation: valueOrZero(a.Recommendation), ProposedSteps: mapInputs(a.ProposedSteps, ProposedStepInput.domain)}
}

func (a ProposedStepInput) domain() work.ProposedStep {
	return work.ProposedStep{Title: a.Title, AcceptanceCriteria: a.AcceptanceCriteria}
}
