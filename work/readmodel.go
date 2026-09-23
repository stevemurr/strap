package work

import (
	"cmp"
	"github.com/stevemurr/strap/identity"
	"slices"
)

// ReadModel is a passive ledger projection. Applying accepted changes never
// dispatches work, emits routing events, executes tools, or modifies a live store.
// Reads reuse the domain's existing visibility rules.
type ReadModel struct{ store *Store }

func NewReadModel() *ReadModel { return &ReadModel{store: New()} }
func (v *ReadModel) Apply(c Change) {
	s := v.store
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range c.Works {
		s.works[w.ID] = w.Clone()
	}
	for _, p := range c.Plans {
		s.plans[p.ID] = p.Clone()
	}
	for _, sub := range c.Submissions {
		s.submissions[sub.ID] = sub.Clone()
	}
	for _, a := range c.Audits {
		s.audits[a.ID] = a.Clone()
	}
	for _, b := range c.ResearchBriefs {
		s.researchBriefs[b.ID] = b.Clone()
	}
	for _, r := range c.ProgressReports {
		s.progressReports[r.ID] = r.Clone()
		for _, f := range r.Findings {
			s.progressFindings[f.ID] = f.Clone()
		}
	}
}
func (v *ReadModel) GetWorkProgressReport(actor identity.ActorID, id ProgressReportID) (WorkProgressReport, error) {
	return v.store.GetWorkProgressReport(actor, id)
}
func (v *ReadModel) GetProgressFinding(actor identity.ActorID, id ProgressFindingID) (ProgressFinding, error) {
	return v.store.GetProgressFinding(actor, id)
}
func (v *ReadModel) GetPlan(actor identity.ActorID, id PlanID) (Plan, error) {
	return v.store.GetPlan(actor, id)
}
func (v *ReadModel) GetWork(actor identity.ActorID, id ID) (Work, error) {
	return v.store.GetWork(actor, id)
}
func (v *ReadModel) GetSubmission(actor identity.ActorID, id SubmissionID) (Submission, error) {
	return v.store.GetSubmission(actor, id)
}
func (v *ReadModel) GetAudit(actor identity.ActorID, id AuditID) (Audit, error) {
	return v.store.GetAudit(actor, id)
}
func (v *ReadModel) InspectWork(actor identity.ActorID, id ID) (Inspection, error) {
	return v.store.InspectWork(actor, id)
}

func (v *Store) InspectWork(actor identity.ActorID, id ID) (Inspection, error) {
	w, err := v.GetWork(actor, id)
	if err != nil {
		return Inspection{}, err
	}
	out := Inspection{Work: w}
	if w.Scope != nil {
		p, err := v.GetPlan(actor, w.Scope.PlanID)
		if err == nil {
			for _, step := range p.Steps {
				if slices.Contains(w.Scope.StepIDs, step.ID) {
					out.Steps = append(out.Steps, step)
				}
			}
		}
	}
	sid := w.LatestSubmissionID
	auditID := w.LatestAuditID
	if w.Kind == AuditWork {
		sid = w.SubjectSubmissionID
	}
	if w.Kind == Repair {
		auditID = w.RequestedByAuditID
	}
	if auditID != "" {
		a, err := v.GetAudit(actor, auditID)
		if err != nil {
			return Inspection{}, err
		}
		out.Audit = &a
		if w.Kind == Repair {
			sid = a.SubmissionID
		}
	}
	if sid != "" {
		sub, err := v.GetSubmission(actor, sid)
		if err != nil {
			return Inspection{}, err
		}
		// The contextual repair view stays scoped even for an owner with wider rights.
		if w.Kind == Repair && w.Scope != nil {
			sub.Steps = slices.DeleteFunc(sub.Steps, func(step Step) bool { return !slices.Contains(w.Scope.StepIDs, step.ID) })
		}
		out.Submission = &sub
	}
	return out, nil
}

// Works returns detached snapshots; authorization belongs to the host's root-only listing.
func (v *ReadModel) Works() []Work {
	v.store.mu.Lock()
	defer v.store.mu.Unlock()
	out := make([]Work, 0, len(v.store.works))
	for _, w := range v.store.works {
		out = append(out, w.Clone())
	}
	slices.SortFunc(out, func(a, b Work) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// OpenStep is a plan step that still needs work: neither completed nor
// cancelled, and in the scope of no live work.
type OpenStep struct {
	PlanID PlanID
	Step   Step
}

// UncoveredSteps lists owner's open steps that no live work has in scope, in
// plan order. Steps complete only when an audit accepts work that scoped them,
// so these steps cannot finish unless they are assigned or cancelled.
func (v *ReadModel) UncoveredSteps(owner identity.ActorID) []OpenStep {
	v.store.mu.Lock()
	defer v.store.mu.Unlock()
	covered := map[StepID]bool{}
	for _, w := range v.store.works {
		if w.Scope != nil && !w.State.Terminal() {
			for _, id := range w.Scope.StepIDs {
				covered[id] = true
			}
		}
	}
	plans := make([]Plan, 0, len(v.store.plans))
	for _, p := range v.store.plans {
		if p.Owner == owner {
			plans = append(plans, p)
		}
	}
	slices.SortFunc(plans, func(a, b Plan) int { return cmp.Compare(a.ID, b.ID) })
	var open []OpenStep
	for _, p := range plans {
		for _, s := range p.Steps {
			if s.Status != Completed && s.Status != CancelledStep && !covered[s.ID] {
				open = append(open, OpenStep{PlanID: p.ID, Step: s})
			}
		}
	}
	return open
}
