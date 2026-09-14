package work

import (
	"cmp"
	"slices"
	"time"

	"github.com/stevemurr/strap/identity"
)

type WorkStateSnapshot struct {
	WorkID             ID               `json:"work_id"`
	Owner              identity.ActorID `json:"owner"`
	Assignee           identity.ActorID `json:"assignee"`
	State              State            `json:"state"`
	WorkRevision       Revision         `json:"work_revision"`
	AssignedAtRevision Revision         `json:"assigned_at_revision"`
	ActiveBlocker      string           `json:"active_blocker,omitempty"`
	CurrentNote        string           `json:"current_note,omitempty"`
}
type ReportedPosition struct {
	Value              WorkPosition     `json:"value"`
	ReportID           ProgressReportID `json:"report_id"`
	RecordedAt         time.Time        `json:"recorded_at"`
	AssignedAtRevision Revision         `json:"assigned_at_revision"`
}

// WorkProgress is a trusted typed snapshot. Model adapters must bound and page
// findings and large records before exposing this value to a provider.
type WorkProgress struct {
	Current              WorkStateSnapshot `json:"current"`
	LatestReportID       ProgressReportID  `json:"latest_report_id,omitempty"`
	LatestReportedAt     *time.Time        `json:"latest_reported_at,omitempty"`
	LastReportedPosition *ReportedPosition `json:"last_reported_position,omitempty"`
	Findings             []ProgressFinding `json:"findings,omitempty"`
	Steps                []Step            `json:"steps,omitempty"`
	MissingStepIDs       []StepID          `json:"missing_step_ids,omitempty"`
}

func (s *Store) GetWorkProgress(actor identity.ActorID, id ID) (WorkProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.works[id]
	if !ok {
		return WorkProgress{}, ErrNotFound
	}
	if actor == "" || actor != w.Owner && actor != w.Assignee {
		return WorkProgress{}, ErrForbidden
	}
	v := WorkProgress{Current: WorkStateSnapshot{WorkID: id, Owner: w.Owner, Assignee: w.Assignee, State: w.State, WorkRevision: w.Revision, AssignedAtRevision: w.AssignedAtRevision, ActiveBlocker: w.Blocker, CurrentNote: w.Note}, LatestReportID: w.LatestProgressReportID}
	if r, ok := s.progressReports[w.LatestProgressReportID]; ok {
		v.LatestReportedAt = &r.RecordedAt
	}
	if r, ok := s.progressReports[w.LatestPositionReportID]; ok && r.Position != nil {
		v.LastReportedPosition = &ReportedPosition{Value: *clonePosition(r.Position), ReportID: r.ID, RecordedAt: r.RecordedAt, AssignedAtRevision: r.AssignedAtRevision}
	}
	superseded := map[ProgressFindingID]bool{}
	for _, f := range s.progressFindings {
		if f.WorkID == id && f.Supersedes != "" {
			superseded[f.Supersedes] = true
		}
	}
	for _, f := range s.progressFindings {
		if f.WorkID == id && !superseded[f.ID] {
			v.Findings = append(v.Findings, f.Clone())
		}
	}
	slices.SortFunc(v.Findings, func(a, b ProgressFinding) int { return cmp.Compare(a.ID, b.ID) })
	// Work visibility grants only the referenced steps, including after closure.
	// It does not grant the worker a broad GetPlan capability.
	v.Steps = s.steps(w.Scope)
	if w.Scope != nil {
		for _, id := range w.Scope.StepIDs {
			if !slices.ContainsFunc(v.Steps, func(s Step) bool { return s.ID == id }) {
				v.MissingStepIDs = append(v.MissingStepIDs, id)
			}
		}
	}
	return v, nil
}

func (v *ReadModel) GetWorkProgress(actor identity.ActorID, id ID) (WorkProgress, error) {
	return v.store.GetWorkProgress(actor, id)
}
