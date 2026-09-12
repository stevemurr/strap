package work

import (
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
	if w.Kind == AuditWork {
		sid = w.SubjectSubmissionID
	}
	if sid != "" {
		sub, err := v.GetSubmission(actor, sid)
		if err != nil {
			return Inspection{}, err
		}
		out.Submission = &sub
	}
	if w.RequestedByAuditID != "" {
		a, err := v.GetAudit(actor, w.RequestedByAuditID)
		if err != nil {
			return Inspection{}, err
		}
		out.Audit = &a
	}
	return out, nil
}
