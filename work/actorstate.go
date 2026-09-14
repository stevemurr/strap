package work

import (
	"slices"
	"strings"

	"github.com/stevemurr/strap/identity"
)

// ActorState is the harness-owned state an agent needs at the start of an
// exchange: the ids, revisions and statuses it would otherwise have to remember
// from earlier receipts or guess. It is computed from the store on every wake,
// so it is never stale and never contains an invented id. Titles are shortened;
// cancelled steps and terminal work are omitted.
type ActorState struct {
	Plans    []PlanState `json:"plans,omitempty"`
	Owned    []WorkState `json:"owned_work,omitempty"`
	Assigned []WorkState `json:"assigned_work,omitempty"`
}
type PlanState struct {
	PlanID   PlanID      `json:"plan_id"`
	Revision Revision    `json:"revision"`
	Title    string      `json:"title"`
	Steps    []StepState `json:"steps,omitempty"`
}
type StepState struct {
	StepID     StepID     `json:"step_id"`
	Status     StepStatus `json:"status"`
	Title      string     `json:"title"`
	ReservedBy ID         `json:"reserved_by,omitempty"`
}
type WorkState struct {
	WorkID                ID               `json:"work_id"`
	Kind                  Kind             `json:"kind"`
	State                 State            `json:"state"`
	Revision              Revision         `json:"revision"`
	AssignedAtRevision    Revision         `json:"assigned_at_revision,omitempty"`
	Assignee              identity.ActorID `json:"assignee,omitempty"`
	Blocker               string           `json:"blocker,omitempty"`
	Steps                 []StepState      `json:"steps,omitempty"`
	SubjectSubmissionID   SubmissionID     `json:"subject_submission_id,omitempty"`
	RequestedByAuditID    AuditID          `json:"requested_by_audit_id,omitempty"`
	LatestSubmissionID    SubmissionID     `json:"latest_submission_id,omitempty"`
	LatestAuditID         AuditID          `json:"latest_audit_id,omitempty"`
	ActiveRepairID        ID               `json:"active_repair_id,omitempty"`
	LatestResearchBriefID ResearchBriefID  `json:"latest_research_brief_id,omitempty"`
}

const stateTitleLimit = 80

// ActorState reports plans the actor owns and live work it owns or is
// assigned. Owned entries name the assignee and latest records; assigned
// entries carry the assignment revision and scoped steps.
func (s *Store) ActorState(actor identity.ActorID) ActorState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actorState(actor)
}
func (v *ReadModel) ActorState(actor identity.ActorID) ActorState { return v.store.ActorState(actor) }

func (s *Store) actorState(actor identity.ActorID) ActorState {
	var out ActorState
	if actor == "" {
		return out
	}
	reserved := map[StepID]ID{}
	for _, w := range s.works {
		if live(w) && w.Scope != nil {
			for _, id := range w.Scope.StepIDs {
				reserved[id] = w.ID
			}
		}
	}
	for _, p := range s.plans {
		if p.Owner != actor {
			continue
		}
		plan := PlanState{PlanID: p.ID, Revision: p.Revision, Title: shorten(p.Title)}
		for _, step := range p.Steps {
			if step.Status != CancelledStep {
				plan.Steps = append(plan.Steps, StepState{StepID: step.ID, Status: step.Status, Title: shorten(step.Title), ReservedBy: reserved[step.ID]})
			}
		}
		out.Plans = append(out.Plans, plan)
	}
	for _, w := range s.works {
		if !live(w) {
			continue
		}
		if w.Owner == actor {
			out.Owned = append(out.Owned, s.workState(w, true))
		}
		if w.Assignee == actor {
			out.Assigned = append(out.Assigned, s.workState(w, false))
		}
	}
	slices.SortFunc(out.Plans, func(a, b PlanState) int { return strings.Compare(string(a.PlanID), string(b.PlanID)) })
	slices.SortFunc(out.Owned, func(a, b WorkState) int { return strings.Compare(string(a.WorkID), string(b.WorkID)) })
	slices.SortFunc(out.Assigned, func(a, b WorkState) int { return strings.Compare(string(a.WorkID), string(b.WorkID)) })
	return out
}

func (s *Store) workState(w Work, owned bool) WorkState {
	state := WorkState{
		WorkID: w.ID, Kind: w.Kind, State: w.State, Revision: w.Revision, Blocker: shorten(w.Blocker),
		SubjectSubmissionID: w.SubjectSubmissionID, RequestedByAuditID: w.RequestedByAuditID,
		LatestSubmissionID: w.LatestSubmissionID, LatestAuditID: w.LatestAuditID,
		ActiveRepairID: w.ActiveRepairID, LatestResearchBriefID: w.LatestResearchBriefID,
	}
	if owned {
		state.Assignee = w.Assignee
	} else {
		state.AssignedAtRevision = w.AssignedAtRevision
		for _, step := range s.steps(w.Scope) {
			state.Steps = append(state.Steps, StepState{StepID: step.ID, Status: step.Status, Title: shorten(step.Title)})
		}
	}
	return state
}

func shorten(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= stateTitleLimit {
		return text
	}
	cut := stateTitleLimit
	for cut > 0 && cut < len(text) && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut] + "…"
}
