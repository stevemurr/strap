package work

import (
	"encoding/json"
	"slices"

	"github.com/stevemurr/strap/identity"
)

func (s *Store) SubmitWork(actor identity.ActorID, r SubmitRequest) (Submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.target(actor, r.WorkTarget, false)
	if err != nil {
		return Submission{}, err
	}
	if w.State != Active || (w.Kind != Implementation && w.Kind != Repair) {
		return Submission{}, ErrState
	}
	if !blank(w.Blocker) || blank(r.Summary) {
		return Submission{}, invalid("clear blocker and provide summary before submitting")
	}
	for _, artifact := range r.Artifacts {
		if blank(artifact.URI) {
			return Submission{}, invalid("artifact URI required")
		}
	}
	original := w
	if w.Kind == Repair {
		original = s.works[w.ParentID].Clone()
		if original.State != ChangesRequested {
			return Submission{}, ErrState
		}
	}
	steps := s.steps(original.Scope)
	for _, step := range steps {
		if step.Status != ReadyForReview {
			return Submission{}, invalid("all scoped steps must be ready_for_review")
		}
	}
	sub := Submission{ID: SubmissionID(s.id("submission")), WorkID: original.ID, SubmittedVia: w.ID, SubmittedBy: actor, Supersedes: original.LatestSubmissionID, Task: original.Task, ExpectedOutput: original.ExpectedOutput, Steps: steps, Summary: r.Summary, Evidence: r.Evidence, Artifacts: r.Artifacts}.Clone()
	if w.Kind == Repair {
		w.State = Closed
		w.Revision++
		s.works[w.ID] = w
	}
	original.State = NeedsCheck
	original.LatestSubmissionID = sub.ID
	original.Revision++
	original.Blocker = ""
	s.works[original.ID] = original
	s.submissions[sub.ID] = sub
	s.emit(ReviewRequested, actor, original, true)
	return s.submissionView(actor, sub), nil
}

// Defense in depth for host integrations: a contributor anywhere in a submission
// chain must not review it, even if work has since changed assignees.
func (s *Store) contributor(actor identity.ActorID, id SubmissionID) bool {
	for id != "" {
		sub, ok := s.submissions[id]
		if !ok {
			break
		}
		if sub.SubmittedBy == actor {
			return true
		}
		id = sub.Supersedes
	}
	return false
}
func (s *Store) AssignAudit(actor identity.ActorID, r AssignAuditRequest) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	original, err := s.target(actor, r.WorkTarget, true)
	if err != nil {
		return Work{}, err
	}
	if original.Kind != Implementation || original.State != NeedsCheck || original.LatestSubmissionID != r.SubmissionID || r.SubmissionID == "" {
		return Work{}, ErrState
	}
	if blank(string(r.Auditor)) || r.Auditor == original.Assignee || s.contributor(r.Auditor, r.SubmissionID) {
		return Work{}, ErrForbidden
	}
	w := Work{ID: ID(s.id("work")), Kind: AuditWork, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: original.Owner, RequestedBy: actor, Assignee: r.Auditor, Task: "Audit the submitted outcome: " + original.Task, ExpectedOutput: "Submit a pass or fail verdict with evidence. If unable to verify, report a blocker.", Scope: original.Scope, ParentID: original.ID, SubjectSubmissionID: r.SubmissionID}.Clone()
	original.State = Checking
	original.Revision++
	s.works[original.ID] = original
	s.works[w.ID] = w
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}
func (s *Store) SubmitAudit(actor identity.ActorID, r AuditRequest) (Audit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.target(actor, r.WorkTarget, false)
	if err != nil {
		return Audit{}, err
	}
	if w.Kind != AuditWork || w.State != Active || r.SubmissionID == "" || w.SubjectSubmissionID != r.SubmissionID {
		return Audit{}, ErrState
	}
	original := s.works[w.ParentID].Clone()
	if original.State != Checking || original.LatestSubmissionID != r.SubmissionID {
		return Audit{}, ErrState
	}
	if !blank(w.Blocker) || blank(r.Summary) {
		return Audit{}, invalid("clear blocker and provide audit summary")
	}
	if r.Verdict != Pass && r.Verdict != Fail {
		return Audit{}, invalid("verdict must be pass or fail")
	}
	if (r.Verdict == Pass && len(r.Findings) != 0) || (r.Verdict == Fail && len(r.Findings) == 0) {
		return Audit{}, invalid("only a failing verdict requires findings")
	}
	affected := map[StepID]bool{}
	for _, f := range r.Findings {
		if blank(f.Description) || blank(f.RequiredChange) || blank(f.Verification) {
			return Audit{}, invalid("findings require description, change and verification")
		}
		if original.Scope != nil && len(f.StepIDs) == 0 {
			return Audit{}, invalid("finding requires scoped steps")
		}
		for _, id := range f.StepIDs {
			if original.Scope == nil || !slices.Contains(original.Scope.StepIDs, id) {
				return Audit{}, ErrForbidden
			}
			affected[id] = true
		}
	}
	a := Audit{ID: AuditID(s.id("audit")), WorkID: w.ID, SubmissionID: r.SubmissionID, ReviewedBy: actor, Verdict: r.Verdict, Summary: r.Summary, Findings: r.Findings}.Clone()
	var p Plan
	if original.Scope != nil {
		p = s.plans[original.Scope.PlanID].Clone()
	}
	if r.Verdict == Pass {
		original.State = Accepted
		for i := range p.Steps {
			if slices.Contains(original.Scope.StepIDs, p.Steps[i].ID) {
				p.Steps[i].Status = Completed
				delete(s.reserved, p.Steps[i].ID)
			}
		}
	} else {
		original.State = ChangesRequested
		var scope *Scope
		if original.Scope != nil {
			scope = &Scope{PlanID: original.Scope.PlanID}
			for _, id := range original.Scope.StepIDs {
				if affected[id] {
					scope.StepIDs = append(scope.StepIDs, id)
				}
			}
			for i := range p.Steps {
				if affected[p.Steps[i].ID] {
					p.Steps[i].Status = Pending
				}
			}
		}
		findings, _ := json.Marshal(a.Findings)
		repair := Work{ID: ID(s.id("work")), Kind: Repair, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: original.Owner, RequestedBy: actor, Assignee: s.submissions[r.SubmissionID].SubmittedBy, Scope: scope, Task: "Repair the audited outcome: " + original.Task, Context: string(findings), ExpectedOutput: "Address every finding and submit the repaired outcome for another audit.", ParentID: original.ID, RequestedByAuditID: a.ID}.Clone()
		a.RepairWorkID = repair.ID
		s.works[repair.ID] = repair
	}
	if original.Scope != nil {
		s.plans[p.ID] = p
	}
	original.Revision++
	w.State = Closed
	w.Revision++
	s.works[original.ID] = original
	s.works[w.ID] = w
	s.audits[a.ID] = a
	s.emit(AuditCompleted, actor, original, true)
	s.events[len(s.events)-1].AuditID = a.ID
	if a.RepairWorkID != "" {
		s.emit(WorkAssigned, actor, s.works[a.RepairWorkID], true)
	}
	return a.Clone(), nil
}
func (s *Store) Reassign(actor identity.ActorID, r ReassignRequest) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.target(actor, r.WorkTarget, true)
	if err != nil {
		return Work{}, err
	}
	if w.State != Active {
		return Work{}, ErrState
	}
	if blank(string(r.Assignee)) {
		return Work{}, invalid("assignee required")
	}
	if w.Kind == AuditWork && (s.works[w.ParentID].Assignee == r.Assignee || s.contributor(r.Assignee, w.SubjectSubmissionID)) {
		return Work{}, ErrForbidden
	}
	w.Assignee = r.Assignee
	w.Revision++
	w.AssignedAtRevision = w.Revision
	w.Blocker = ""
	s.works[w.ID] = w
	s.emit(WorkReassigned, actor, w, true)
	return w.Clone(), nil
}
func (s *Store) cancelImplementation(actor identity.ActorID, w Work, reason string) {
	for _, child := range s.works {
		if child.ParentID == w.ID && live(child) {
			child.State = Cancelled
			child.Revision++
			child.Note = reason
			s.works[child.ID] = child
			s.emit(WorkCancelled, actor, child, true)
		}
	}
	if w.Scope != nil {
		p := s.plans[w.Scope.PlanID].Clone()
		for i := range p.Steps {
			if s.reserved[p.Steps[i].ID] == w.ID {
				delete(s.reserved, p.Steps[i].ID)
				p.Steps[i].Status = Pending
			}
		}
		s.plans[p.ID] = p
	}
	w.State = Cancelled
	w.Revision++
	w.Note = reason
	s.works[w.ID] = w
	s.emit(WorkCancelled, actor, w, true)
}

// Cancelling repair cancels its implementation cycle. Cancelling an audit alone
// returns the unchanged submission to needs_check so another auditor can review it.
func (s *Store) Cancel(actor identity.ActorID, r CancelRequest) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.target(actor, r.WorkTarget, true)
	if err != nil {
		return Work{}, err
	}
	if !live(w) || blank(r.Reason) {
		return Work{}, ErrState
	}
	if w.Kind == Implementation {
		s.cancelImplementation(actor, w, r.Reason)
	} else if w.Kind == Repair {
		s.cancelImplementation(actor, s.works[w.ParentID], r.Reason)
	} else {
		w.State = Cancelled
		w.Revision++
		w.Note = r.Reason
		s.works[w.ID] = w
		s.emit(WorkCancelled, actor, w, true)
		original := s.works[w.ParentID]
		original.State = NeedsCheck
		original.Revision++
		s.works[original.ID] = original
		s.emit(ReviewRequested, actor, original, true)
	}
	return s.works[w.ID].Clone(), nil
}
