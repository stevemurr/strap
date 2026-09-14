package work

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stevemurr/strap/identity"
)

type ResearchAssignRequest struct {
	Assignee       identity.ActorID `json:"assignee"`
	Task           string           `json:"task"`
	Context        string           `json:"context,omitempty"`
	ExpectedOutput string           `json:"expected_output,omitempty"`
}

func (s *Store) AssignResearch(actor identity.ActorID, r ResearchAssignRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	if blank(string(actor)) || blank(string(r.Assignee)) || blank(r.Task) {
		return result, invalid("actor, assignee and task required")
	}
	w := Work{ID: ID(s.id("work")), Kind: Research, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: actor, RequestedBy: actor, Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput}
	s.putWork(w.ID, w)
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}

type ResearchBriefID string

const ResearchDelivered EventKind = "research_delivered"

type ProposedStep struct {
	Title              string   `json:"title"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
}
type SubmitResearchRequest struct {
	WorkTarget
	AssignedAtRevision Revision            `json:"assigned_at_revision"`
	Summary            string              `json:"summary"`
	FindingIDs         []ProgressFindingID `json:"finding_ids,omitempty"`
	OpenQuestions      []string            `json:"open_questions,omitempty"`
	Recommendation     string              `json:"recommendation,omitempty"`
	ProposedSteps      []ProposedStep      `json:"proposed_steps,omitempty"`
}
type ResearchBrief struct {
	ID                 ResearchBriefID     `json:"brief_id"`
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
type SubmitResearchResult struct {
	WorkID             ID              `json:"work_id"`
	WorkRevision       Revision        `json:"work_revision"`
	AssignedAtRevision Revision        `json:"assigned_at_revision"`
	State              State           `json:"state"`
	BriefID            ResearchBriefID `json:"brief_id"`
	FindingCount       int             `json:"finding_count"`
	RecordedAt         time.Time       `json:"recorded_at"`
}

func (b ResearchBrief) Clone() ResearchBrief {
	b.FindingIDs = slices.Clone(b.FindingIDs)
	b.OpenQuestions = slices.Clone(b.OpenQuestions)
	b.ProposedSteps = slices.Clone(b.ProposedSteps)
	for i := range b.ProposedSteps {
		b.ProposedSteps[i].AcceptanceCriteria = slices.Clone(b.ProposedSteps[i].AcceptanceCriteria)
	}
	return b
}
func (s *Store) SubmitResearch(actor identity.ActorID, r SubmitResearchRequest) (result SubmitResearchResult, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.target(actor, r.WorkTarget, false)
	if err != nil {
		return result, err
	}
	if w.Kind != Research || w.State != Active {
		return result, ErrState
	}
	if r.AssignedAtRevision == 0 || r.AssignedAtRevision != w.AssignedAtRevision {
		return result, ErrConflict
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
	b := ResearchBrief{ID: ResearchBriefID(s.id("brief")), Author: actor, RecordedAt: time.Now().UTC(), WorkID: w.ID, WorkRevision: w.Revision + 1, AssignedAtRevision: w.AssignedAtRevision, Summary: r.Summary, FindingIDs: r.FindingIDs, OpenQuestions: r.OpenQuestions, Recommendation: r.Recommendation, ProposedSteps: r.ProposedSteps}.Clone()
	encoded, err := json.Marshal(b)
	if err != nil {
		return result, err
	}
	if len(encoded) > 64*1024 {
		return result, invalid("encoded brief exceeds 64 KiB")
	}
	w.State, w.Revision, w.LatestResearchBriefID, w.Blocker = Delivered, b.WorkRevision, b.ID, ""
	s.researchBriefs[b.ID] = b
	s.change.ResearchBriefs = append(s.change.ResearchBriefs, b.Clone())
	s.putWork(w.ID, w)
	s.emit(ResearchDelivered, actor, w, true)
	return SubmitResearchResult{WorkID: w.ID, WorkRevision: w.Revision, AssignedAtRevision: w.AssignedAtRevision, State: Delivered, BriefID: b.ID, FindingCount: len(b.FindingIDs), RecordedAt: b.RecordedAt}, nil
}
func (s *Store) GetResearchBrief(actor identity.ActorID, id ResearchBriefID) (ResearchBrief, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.researchBriefs[id]
	if !ok {
		if strings.HasPrefix(string(id), "report-") {
			return ResearchBrief{}, fmt.Errorf("%w: %s is a progress report id, not a brief id; %s", ErrNotFound, id, s.knownBriefs(actor))
		}
		return ResearchBrief{}, fmt.Errorf("%w: research brief %s; %s", ErrNotFound, id, s.knownBriefs(actor))
	}
	w := s.works[b.WorkID]
	if actor == "" || actor != w.Owner && actor != w.Assignee {
		return ResearchBrief{}, ErrForbidden
	}
	return b.Clone(), nil
}
func (v *ReadModel) GetResearchBrief(actor identity.ActorID, id ResearchBriefID) (ResearchBrief, error) {
	return v.store.GetResearchBrief(actor, id)
}
