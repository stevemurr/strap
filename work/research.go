package work

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/stevemurr/strap/identity"
)

// InvestigationRequest assigns review, web research or deep research.
type InvestigationRequest struct {
	Kind           Kind             `json:"kind"`
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
	Scope          *Scope           `json:"scope,omitempty"` // Plan steps the investigation fulfils; delivery completes them.
}

func (s *Store) AssignInvestigation(actor identity.ActorID, r InvestigationRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	if blank(string(actor)) || blank(string(r.Assignee)) || blank(r.Task) {
		return result, invalid("actor, assignee and task required")
	}
	if !r.Kind.Investigation() {
		return result, invalid("investigation kind must be review, web_research or deep_research")
	}
	if err = s.checkScope(actor, r.Scope); err != nil {
		return result, err
	}
	w := Work{ID: ID(s.id("work")), Kind: r.Kind, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: actor, RequestedBy: actor, Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope}.Clone()
	s.reserve(w)
	s.putWork(w.ID, w)
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}

type BriefID string

const BriefDelivered EventKind = "brief_delivered"

type ProposedStep struct {
	Title              string   `json:"title"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}
type SubmitBriefRequest struct {
	WorkTarget
	Summary    string              `json:"summary"`
	FindingIDs []ProgressFindingID `json:"finding_ids,omitempty"`
	// Findings are recorded as a progress report before delivery, and the
	// brief cites them after FindingIDs. The store takes only FindingIDs.
	Findings       []ProgressFindingDraft `json:"findings,omitempty"`
	OpenQuestions  []string               `json:"open_questions,omitempty"`
	Recommendation string                 `json:"recommendation,omitempty"`
	ProposedSteps  []ProposedStep         `json:"proposed_steps,omitempty"`
}
type Brief struct {
	ID                 BriefID             `json:"brief_id"`
	Author             identity.ActorID    `json:"author"`
	RecordedAt         time.Time           `json:"recorded_at"`
	WorkID             ID                  `json:"work_id"`
	WorkRevision       Revision            `json:"work_revision"`
	AssignedAtRevision Revision            `json:"assigned_at_revision"`
	Summary            string              `json:"summary"`
	FindingIDs         []ProgressFindingID `json:"finding_ids,omitempty"`
	OpenQuestions      []string            `json:"open_questions,omitempty"`
	Recommendation     string              `json:"recommendation,omitempty"`
	ProposedSteps      []ProposedStep      `json:"proposed_steps,omitempty"`
}
type SubmitBriefResult struct {
	WorkID             ID        `json:"work_id"`
	WorkRevision       Revision  `json:"work_revision"`
	AssignedAtRevision Revision  `json:"assigned_at_revision"`
	State              State     `json:"state"`
	BriefID            BriefID   `json:"brief_id"`
	FindingCount       int       `json:"finding_count"`
	RecordedAt         time.Time `json:"recorded_at"`
}

func (b Brief) Clone() Brief {
	b.FindingIDs = slices.Clone(b.FindingIDs)
	b.OpenQuestions = slices.Clone(b.OpenQuestions)
	b.ProposedSteps = slices.Clone(b.ProposedSteps)
	for i := range b.ProposedSteps {
		b.ProposedSteps[i].AcceptanceCriteria = slices.Clone(b.ProposedSteps[i].AcceptanceCriteria)
	}
	return b
}
func (s *Store) SubmitBrief(actor identity.ActorID, r SubmitBriefRequest) (result SubmitBriefResult, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.target(actor, r.WorkTarget, false)
	if err != nil {
		return result, err
	}
	if !w.Kind.Investigation() || w.State != Active {
		return result, ErrState
	}
	if len(r.Findings) > 0 {
		return result, invalid("record findings before submitting; the store cites finding_ids only")
	}
	if blank(r.Summary) || len(r.FindingIDs) > 256 || len(r.OpenQuestions) > 32 || len(r.ProposedSteps) > 32 {
		return result, invalid("summary required; at most 256 findings, 32 questions and 32 proposed steps")
	}
	if err = prose(append([]string{r.Summary, r.Recommendation}, r.OpenQuestions...)...); err != nil {
		return result, err
	}
	for _, step := range r.ProposedSteps {
		if blank(step.Title) {
			return result, invalid("proposed step title required")
		}
		if err = prose(append([]string{step.Title}, step.AcceptanceCriteria...)...); err != nil {
			return result, err
		}
	}
	seen := map[ProgressFindingID]bool{}
	for _, id := range r.FindingIDs {
		f, ok := s.progressFindings[id]
		if !ok {
			return result, fmt.Errorf("%w: finding %s was never recorded for %s; finding IDs are issued in report_work_progress receipts, so omit finding_ids if none were recorded", ErrNotFound, id, w.ID)
		}
		if f.WorkID != w.ID {
			return result, ErrForbidden
		}
		if seen[id] {
			return result, invalid("duplicate finding")
		}
		seen[id] = true
		for _, other := range s.progressFindings {
			if other.Supersedes == id {
				return result, invalid("brief must select current finding versions")
			}
		}
	}
	b := Brief{ID: BriefID(s.id("brief")), Author: actor, RecordedAt: time.Now().UTC(), WorkID: w.ID, WorkRevision: w.Revision + 1, AssignedAtRevision: w.AssignedAtRevision, Summary: r.Summary, FindingIDs: r.FindingIDs, OpenQuestions: r.OpenQuestions, Recommendation: r.Recommendation, ProposedSteps: r.ProposedSteps}.Clone()
	encoded, err := json.Marshal(b)
	if err != nil {
		return result, err
	}
	if len(encoded) > 64*1024 {
		return result, invalid("encoded brief exceeds 64 KiB")
	}
	w.State, w.Revision, w.LatestBriefID, w.Blocker = Delivered, b.WorkRevision, b.ID, ""
	// Delivery is research's success, as acceptance is implementation's: it
	// completes the steps the investigation was scoped to.
	s.settleScope(w, Completed)
	s.researchBriefs[b.ID] = b
	s.change.Briefs = append(s.change.Briefs, b.Clone())
	s.putWork(w.ID, w)
	s.emit(BriefDelivered, actor, w, true)
	return SubmitBriefResult{WorkID: w.ID, WorkRevision: w.Revision, AssignedAtRevision: w.AssignedAtRevision, State: Delivered, BriefID: b.ID, FindingCount: len(b.FindingIDs), RecordedAt: b.RecordedAt}, nil
}
func (s *Store) GetBrief(actor identity.ActorID, id BriefID) (Brief, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.researchBriefs[id]
	if !ok {
		if hint, ok := Misrouted(string(id), "brief-"); ok {
			return Brief{}, fmt.Errorf("%w: %s; %s", ErrNotFound, hint, s.knownBriefs(actor))
		}
		return Brief{}, fmt.Errorf("%w: brief %s; %s", ErrNotFound, id, s.knownBriefs(actor))
	}
	w := s.works[b.WorkID]
	if !w.visibleTo(actor) {
		return Brief{}, ErrForbidden
	}
	return b.Clone(), nil
}
func (v *ReadModel) GetBrief(actor identity.ActorID, id BriefID) (Brief, error) {
	return v.store.GetBrief(actor, id)
}
