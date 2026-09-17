package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stevemurr/strap/identity"
)

type ProgressReportID string
type ProgressFindingID string
type FindingBasis string

const (
	Observed             FindingBasis = "observed"
	Inferred             FindingBasis = "inferred"
	Retracted            FindingBasis = "retracted"
	WorkProgressReported EventKind    = "work_progress_reported"
)

type EvidenceRef struct {
	URI      string `json:"uri"`
	Revision string `json:"revision,omitempty"`
	Locator  string `json:"locator,omitempty"`
	Detail   string `json:"detail,omitempty"`
}
type ProgressFindingDraft struct {
	Claim      string            `json:"claim"`
	Basis      FindingBasis      `json:"basis"`
	Evidence   []EvidenceRef     `json:"evidence,omitempty"`
	Limitation string            `json:"limitation,omitempty"`
	Supersedes ProgressFindingID `json:"supersedes,omitempty"`
}
type ProgressFinding struct {
	ID         ProgressFindingID `json:"finding_id"`
	WorkID     ID                `json:"work_id"`
	ReportID   ProgressReportID  `json:"report_id"`
	Author     identity.ActorID  `json:"author"`
	Claim      string            `json:"claim"`
	Basis      FindingBasis      `json:"basis"`
	Evidence   []EvidenceRef     `json:"evidence,omitempty"`
	Limitation string            `json:"limitation,omitempty"`
	Supersedes ProgressFindingID `json:"supersedes,omitempty"`
}
type ProgressDependency struct {
	Need                    string `json:"need"`
	WorkID                  ID     `json:"work_id,omitempty"`
	PreventsFurtherProgress bool   `json:"prevents_further_progress,omitempty"`
}
type WorkPosition struct {
	Objective    string               `json:"objective"`
	Activity     string               `json:"activity,omitempty"`
	Note         string               `json:"note,omitempty"`
	NextStep     string               `json:"next_step,omitempty"`
	Uncertainty  string               `json:"uncertainty,omitempty"`
	Blocker      string               `json:"blocker,omitempty"`
	DecisionNeed string               `json:"decision_need,omitempty"`
	Dependencies []ProgressDependency `json:"dependencies,omitempty"`
}

// ReportWorkProgressRequest names its work and nothing else. A report adds to a
// record only its assignee writes, so there is no update for a revision to
// protect: an owner who cancels or reassigns the work is caught by the state and
// assignee checks instead. Requiring a counter that changes on every call cost
// far more rejections than the conflicts it could find.
type ReportWorkProgressRequest struct {
	WorkID   ID                     `json:"work_id"`
	Position *WorkPosition          `json:"position,omitempty"`
	Findings []ProgressFindingDraft `json:"findings,omitempty"`
	Steps    []StepProgress         `json:"steps,omitempty"`
}
type WorkProgressReport struct {
	ID                 ProgressReportID  `json:"report_id"`
	WorkID             ID                `json:"work_id"`
	Author             identity.ActorID  `json:"author"`
	AssignedAtRevision Revision          `json:"assigned_at_revision"`
	WorkRevision       Revision          `json:"work_revision"`
	RecordedAt         time.Time         `json:"recorded_at"`
	Position           *WorkPosition     `json:"position,omitempty"`
	Findings           []ProgressFinding `json:"findings,omitempty"`
	Steps              []StepProgress    `json:"steps,omitempty"`
}
type ReportWorkProgressResult struct {
	WorkID             ID                  `json:"work_id"`
	WorkRevision       Revision            `json:"work_revision"`
	AssignedAtRevision Revision            `json:"assigned_at_revision"`
	ReportID           ProgressReportID    `json:"report_id"`
	RecordedAt         time.Time           `json:"recorded_at"`
	FindingIDs         []ProgressFindingID `json:"finding_ids,omitempty"`
}

func clonePosition(p *WorkPosition) *WorkPosition {
	if p == nil {
		return nil
	}
	v := *p
	v.Dependencies = slices.Clone(p.Dependencies)
	return &v
}
func (f ProgressFinding) Clone() ProgressFinding {
	f.Evidence = slices.Clone(f.Evidence)
	return f
}
func (r WorkProgressReport) Clone() WorkProgressReport {
	r.Position = clonePosition(r.Position)
	r.Findings = slices.Clone(r.Findings)
	for i := range r.Findings {
		r.Findings[i] = r.Findings[i].Clone()
	}
	r.Steps = slices.Clone(r.Steps)
	for i := range r.Steps {
		if r.Steps[i].Status != nil {
			v := *r.Steps[i].Status
			r.Steps[i].Status = &v
		}
		if r.Steps[i].Note != nil {
			v := *r.Steps[i].Note
			r.Steps[i].Note = &v
		}
	}
	return r
}
func prose(values ...string) error {
	for _, v := range values {
		if len(v) > 4096 {
			return invalid("progress prose exceeds 4 KiB")
		}
	}
	return nil
}

// ReportWorkProgress commits a report and its authorized step changes together.
// Legacy mutation entry points are migrated separately; this command never infers
// an assignment token or converts a partial note patch into a replacement position.
func (s *Store) ReportWorkProgress(actor identity.ActorID, u ReportWorkProgressRequest) (result ReportWorkProgressResult, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.assigned(actor, u.WorkID, false)
	if err != nil {
		return result, err
	}
	if w.State != Active {
		return result, ErrState
	}
	if u.Position == nil && len(u.Findings) == 0 && len(u.Steps) == 0 {
		return result, invalid("position, finding, or step required")
	}
	if len(u.Findings) > 16 {
		return result, invalid("at most 16 findings per report")
	}
	if u.Position != nil {
		p := u.Position
		if blank(p.Objective) {
			return result, invalid("position objective required")
		}
		if err := prose(p.Objective, p.Activity, p.Note, p.NextStep, p.Uncertainty, p.Blocker, p.DecisionNeed); err != nil {
			return result, err
		}
		for _, d := range p.Dependencies {
			if blank(d.Need) {
				return result, invalid("dependency need required")
			}
			if err := prose(d.Need); err != nil {
				return result, err
			}
			if d.WorkID != "" {
				dep, ok := s.works[d.WorkID]
				if !ok {
					return result, fmt.Errorf("%w: dependency names work %s; %s", ErrNotFound, d.WorkID, s.knownWorks(actor))
				}
				if dep.Owner != actor && dep.Assignee != actor {
					return result, fmt.Errorf("%w: dependency names work %s, which you neither own nor are assigned", ErrForbidden, d.WorkID)
				}
			}
		}
	}
	// Validate against immutable versions before changing any ledger records.
	superseded := map[ProgressFindingID]bool{}
	for _, f := range s.progressFindings {
		if f.Supersedes != "" {
			superseded[f.Supersedes] = true
		}
	}
	for _, f := range u.Findings {
		if blank(f.Claim) {
			return result, invalid("finding claim required")
		}
		if err := prose(f.Claim, f.Limitation); err != nil {
			return result, err
		}
		switch f.Basis {
		case Observed:
			if len(f.Evidence) == 0 {
				return result, invalid("observed finding requires evidence")
			}
		case Inferred:
			if blank(f.Limitation) {
				return result, invalid("inferred finding requires limitation")
			}
		case Retracted:
			if f.Supersedes == "" {
				return result, invalid("retraction requires a target")
			}
		default:
			return result, invalid("unknown finding basis")
		}
		if len(f.Evidence) > 8 {
			return result, invalid("at most 8 evidence references per finding")
		}
		for _, e := range f.Evidence {
			// An execution URI is a host-issued receipt, so a model that has
			// none composes one that looks plausible. Naming the URI and where
			// real ones come from is the difference between a repairable
			// rejection and an unattributable one.
			if strings.HasPrefix(e.URI, "execution:") {
				if s.evidenceLookup == nil {
					return result, fmt.Errorf("%w: %s cites execution evidence, which this session does not record; cite the file or output you actually read", ErrNotFound, e.URI)
				}
				evidence, err := s.evidenceLookup(e.URI)
				if errors.Is(err, ErrNotFound) {
					return result, fmt.Errorf("%w: no execution evidence %s; an execution URI must be an evidence_ref returned by a diagnostic shell result, never one composed by hand; cite the file or output you read instead", ErrNotFound, e.URI)
				}
				if err != nil {
					return result, fmt.Errorf("execution evidence %s: %w", e.URI, err)
				}
				if evidence.WorkID != w.ID || evidence.AssignedAtRevision == 0 || evidence.AssignedAtRevision > w.AssignedAtRevision || evidence.AssignedAtRevision == w.AssignedAtRevision && evidence.Actor != actor {
					return result, fmt.Errorf("%w: execution evidence %s belongs to another assignment; cite only evidence issued to this one", ErrForbidden, e.URI)
				}
			}
			if blank(e.URI) {
				return result, invalid("evidence URI required")
			}
			if err := prose(e.URI, e.Revision, e.Locator, e.Detail); err != nil {
				return result, err
			}
		}
		if f.Supersedes != "" {
			prior, ok := s.progressFindings[f.Supersedes]
			if !ok {
				return result, fmt.Errorf("%w: no finding %s; supersedes takes a finding_id from an earlier report_work_progress receipt", ErrNotFound, f.Supersedes)
			}
			if prior.WorkID != w.ID {
				return result, fmt.Errorf("%w: finding %s belongs to other work", ErrForbidden, f.Supersedes)
			}
			if superseded[prior.ID] {
				return result, invalid("finding already superseded")
			}
			superseded[prior.ID] = true
		}
	}
	p, err := s.progressSteps(w, u.Steps)
	if err != nil {
		return result, err
	}
	r := WorkProgressReport{ID: ProgressReportID(s.id("report")), WorkID: w.ID, Author: actor, AssignedAtRevision: w.AssignedAtRevision, WorkRevision: w.Revision + 1, RecordedAt: time.Now().UTC(), Position: u.Position, Steps: u.Steps}
	for _, f := range u.Findings {
		r.Findings = append(r.Findings, ProgressFinding{ID: ProgressFindingID(s.id("finding")), WorkID: w.ID, ReportID: r.ID, Author: actor, Claim: f.Claim, Basis: f.Basis, Evidence: f.Evidence, Limitation: f.Limitation, Supersedes: f.Supersedes})
	}
	r = r.Clone()
	encoded, err := json.Marshal(r)
	if err != nil {
		return result, err
	}
	if len(encoded) > 64*1024 {
		return result, invalid("encoded report exceeds 64 KiB")
	}
	actionable := false
	if r.Position != nil {
		oldDecision := ""
		if prior, ok := s.progressReports[w.LatestPositionReportID]; ok && prior.Position != nil {
			oldDecision = prior.Position.DecisionNeed
		}
		actionable = r.Position.Blocker != w.Blocker || r.Position.DecisionNeed != oldDecision
		w.Note, w.Blocker = r.Position.Note, r.Position.Blocker
		w.LatestPositionReportID = r.ID
	}
	w.Revision, w.LatestProgressReportID = r.WorkRevision, r.ID
	if len(u.Steps) > 0 {
		s.putPlan(p.ID, p)
	}
	s.progressReports[r.ID] = r
	for _, f := range r.Findings {
		s.progressFindings[f.ID] = f.Clone()
		result.FindingIDs = append(result.FindingIDs, f.ID)
	}
	s.change.ProgressReports = append(s.change.ProgressReports, r.Clone())
	s.putWork(w.ID, w)
	s.emit(WorkProgressReported, actor, w, actionable)
	result.WorkID, result.WorkRevision, result.AssignedAtRevision = w.ID, w.Revision, w.AssignedAtRevision
	result.ReportID, result.RecordedAt = r.ID, r.RecordedAt
	return result, nil
}

// progressSteps prepares a detached plan; validation failures cannot partially
// update shared steps. Both mutation paths use exactly the same scope rules.
func (s *Store) progressSteps(w Work, changes []StepProgress) (Plan, error) {
	if len(changes) > 0 && (w.Kind != Implementation && w.Kind != Repair || w.Scope == nil) {
		return Plan{}, ErrForbidden
	}
	var p Plan
	if w.Scope != nil {
		p = s.plans[w.Scope.PlanID].Clone()
	}
	seen := map[StepID]bool{}
	for _, change := range changes {
		if seen[change.ID] {
			return Plan{}, invalid("repeated progress step")
		}
		seen[change.ID] = true
		if !slices.Contains(w.Scope.StepIDs, change.ID) {
			return Plan{}, ErrForbidden
		}
		if change.Status != nil && *change.Status != Pending && *change.Status != InProgress && *change.Status != Blocked && *change.Status != ReadyForReview {
			return Plan{}, invalid("progress cannot accept or cancel work")
		}
		i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == change.ID })
		if i < 0 {
			return Plan{}, fmt.Errorf("%w: step %s is not in plan %s", ErrNotFound, change.ID, p.ID)
		}
		if change.Status != nil {
			p.Steps[i].Status = *change.Status
		}
		if change.Note != nil {
			if err := prose(*change.Note); err != nil {
				return Plan{}, err
			}
			p.Steps[i].Note = *change.Note
		}
	}
	return p, nil
}

func (s *Store) GetWorkProgressReport(actor identity.ActorID, id ProgressReportID) (WorkProgressReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.progressReports[id]
	if !ok {
		return WorkProgressReport{}, ErrNotFound
	}
	w := s.works[r.WorkID]
	if actor == "" || actor != w.Owner && actor != w.Assignee {
		return WorkProgressReport{}, ErrForbidden
	}
	return r.Clone(), nil
}
func (s *Store) GetProgressFinding(actor identity.ActorID, id ProgressFindingID) (ProgressFinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.progressFindings[id]
	if !ok {
		return ProgressFinding{}, ErrNotFound
	}
	w := s.works[f.WorkID]
	if actor == "" || actor != w.Owner && actor != w.Assignee {
		return ProgressFinding{}, ErrForbidden
	}
	return f.Clone(), nil
}
