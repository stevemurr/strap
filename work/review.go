package work

import (
	"fmt"
	"slices"
	"strings"

	"github.com/stevemurr/strap/identity"
)

func (s *Store) SubmitWork(actor identity.ActorID, r SubmitRequest) (result SubmitReceipt, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.target(actor, r.WorkTarget, false)
	if err != nil {
		return SubmitReceipt{}, err
	}
	if w.State != Active || (w.Kind != Implementation && w.Kind != Repair) {
		return SubmitReceipt{}, ErrState
	}
	if !blank(w.Blocker) || blank(r.Summary) {
		return SubmitReceipt{}, invalid("clear blocker and provide summary before submitting")
	}
	for _, artifact := range r.Artifacts {
		if blank(artifact.URI) {
			return SubmitReceipt{}, invalid("artifact URI required")
		}
	}
	original := w
	if w.Kind == Repair {
		original = s.works[w.ParentID].Clone()
		if original.State != ChangesRequested || original.ActiveRepairID != w.ID {
			return SubmitReceipt{}, ErrState
		}
	}
	// Submitting says the scoped steps are ready for review, so it marks them;
	// implementors otherwise spent a model turn reporting exactly that. A step
	// the implementor reported blocked, or one in any other state, still stops
	// the submission.
	var stuck []string
	for _, step := range s.steps(original.Scope) {
		if step.Status != Pending && step.Status != InProgress && step.Status != ReadyForReview {
			stuck = append(stuck, fmt.Sprintf("%s is %s", step.ID, step.Status))
		}
	}
	if len(stuck) > 0 {
		return SubmitReceipt{}, invalid("scoped steps cannot be submitted: " + strings.Join(stuck, ", ") + "; report blocked steps unblocked through report_work_progress steps, then submit with the returned work_revision")
	}
	if original.Scope != nil {
		p := s.plans[original.Scope.PlanID].Clone()
		for i := range p.Steps {
			if slices.Contains(original.Scope.StepIDs, p.Steps[i].ID) {
				p.Steps[i].Status = ReadyForReview
			}
		}
		s.putPlan(p.ID, p)
	}
	steps := s.steps(original.Scope)
	sub := Submission{ID: SubmissionID(s.id("submission")), WorkID: original.ID, SubmittedVia: w.ID, SubmittedBy: actor, Supersedes: original.LatestSubmissionID, Task: original.Task, ExpectedOutput: original.ExpectedOutput, Steps: steps, Summary: r.Summary, Evidence: r.Evidence, Artifacts: r.Artifacts}.Clone()
	if w.Kind == Repair {
		w.State = Closed
		w.Revision++
		s.putWork(w.ID, w)
	}
	original.ActiveRepairID = ""
	original.State = NeedsCheck
	original.LatestSubmissionID = sub.ID
	original.Revision++
	original.Blocker = ""
	s.putWork(original.ID, original)
	s.putSubmission(sub.ID, sub)
	s.emit(ReviewRequested, actor, original, true)
	return SubmitReceipt{Submission: s.submissionView(actor, sub), WorkRevision: original.Revision}, nil
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

// AuditTask is the task an audit of submission records. Scoped work is
// audited against the acceptance criteria of its plan steps, which the
// auditor reads with the assignment; the implementor's task, which can carry
// how the manager wanted it built, is left out so the audit checks what the
// work must do rather than how it was done (ladder easy-18, 2026-09-25).
// Unscoped work has no criteria, so its task is the requirement.
func AuditTask(original Work, submission SubmissionID) string {
	if original.Scope != nil && len(original.Scope.StepIDs) > 0 {
		return fmt.Sprintf("Audit submission %s of work %s against the acceptance criteria of its scoped plan steps, and the README where they defer to it.", submission, original.ID)
	}
	return "Audit the submitted outcome: " + original.Task
}

// firstWorkHeldBy returns the lowest-numbered work actor was ever assigned, or
// "" when it has held none.
func (s *Store) firstWorkHeldBy(actor identity.ActorID) ID {
	var first ID
	for _, w := range s.works {
		if w.Assignee == actor && (first == "" || w.ID < first) {
			first = w.ID
		}
	}
	return first
}
func (s *Store) AssignAudit(actor identity.ActorID, r AssignAuditRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
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
	// Every audit gets a new auditor. One that already held work carries its
	// context: re-auditing a repair, the auditor of the earlier submission
	// re-ran its earlier tests instead of checking the contract again (ladder
	// medium-01 and medium-19, 2026-09-25).
	if held := s.firstWorkHeldBy(r.Auditor); held != "" {
		return Work{}, fmt.Errorf("%w: %s already held %s; create a new auditor for every audit", ErrForbidden, r.Auditor, held)
	}
	if len(r.Brief) > 64*1024 {
		return Work{}, invalid("audit brief exceeds 64 KiB")
	}
	w := Work{ID: ID(s.id("work")), Kind: AuditWork, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: original.Owner, RequestedBy: actor, Assignee: r.Auditor, Task: AuditTask(original, r.SubmissionID), Context: r.Brief, ExpectedOutput: "Submit a pass or fail verdict with evidence. If unable to verify, report a blocker.", Scope: original.Scope, ParentID: original.ID, SubjectSubmissionID: r.SubmissionID}.Clone()
	original.State = Checking
	original.Revision++
	s.putWork(original.ID, original)
	s.putWork(w.ID, w)
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}
func (s *Store) SubmitAudit(actor identity.ActorID, r AuditRequest) (result Audit, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
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
		for i := range p.Steps {
			if affected[p.Steps[i].ID] {
				p.Steps[i].Status = Pending
			}
		}
	}
	if original.Scope != nil {
		s.putPlan(p.ID, p)
	}
	original.LatestAuditID = a.ID
	original.Revision++
	w.State = Closed
	w.Revision++
	s.putWork(original.ID, original)
	s.putWork(w.ID, w)
	s.putAudit(a.ID, a)
	s.emit(AuditCompleted, actor, original, true)
	s.events[len(s.events)-1].AuditID = a.ID
	return a.Clone(), nil
}

// AssignRepair starts one repair for the current failing verdict. Verdicts remain
// immutable, and validation finishes before any work or revision is changed.
func (s *Store) AssignRepair(actor identity.ActorID, r AssignRepairRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	original, err := s.target(actor, r.WorkTarget, true)
	if err != nil {
		return Work{}, err
	}
	if blank(string(r.Assignee)) || blank(string(r.AuditID)) {
		return Work{}, invalid("assignee and audit_id are required")
	}
	if original.Kind != Implementation || original.State != ChangesRequested {
		return Work{}, ErrState
	}
	if original.ActiveRepairID != "" {
		return Work{}, fmt.Errorf("%w: repair %s is already active", ErrConflict, original.ActiveRepairID)
	}
	a, ok := s.audits[r.AuditID]
	if !ok {
		return Work{}, ErrNotFound
	}
	if a.Verdict != Fail || original.LatestAuditID != a.ID || original.LatestSubmissionID != a.SubmissionID || s.works[a.WorkID].ParentID != original.ID {
		return Work{}, fmt.Errorf("%w: audit does not identify the current failing submission", ErrConflict)
	}
	for _, w := range s.works {
		if w.RequestedByAuditID == a.ID {
			return Work{}, fmt.Errorf("%w: repair %s already exists for this audit", ErrConflict, w.ID)
		}
	}
	var scope *Scope
	if original.Scope != nil {
		scope = &Scope{PlanID: original.Scope.PlanID}
		affected := map[StepID]bool{}
		for _, f := range a.Findings {
			for _, id := range f.StepIDs {
				affected[id] = true
			}
		}
		for _, id := range original.Scope.StepIDs {
			if affected[id] {
				scope.StepIDs = append(scope.StepIDs, id)
			}
		}
	}
	repair := Work{ID: ID(s.id("work")), Kind: Repair, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: original.Owner, RequestedBy: actor, Assignee: r.Assignee, Scope: scope, Task: "Repair the audited outcome: " + original.Task, Context: original.Context, ExpectedOutput: original.ExpectedOutput + "\nAddress every audit finding and submit the repaired outcome for another audit.", ParentID: original.ID, RequestedByAuditID: a.ID}.Clone()
	original.ActiveRepairID = repair.ID
	original.Revision++
	s.putWork(original.ID, original)
	s.putWork(repair.ID, repair)
	s.emit(WorkAssigned, actor, repair, true)
	return repair.Clone(), nil
}

func (s *Store) cancelImplementation(actor identity.ActorID, w Work, reason string) {
	for _, child := range s.works {
		if child.ParentID == w.ID && live(child) {
			s.cancelWork(actor, child, reason)
		}
	}
	s.settleScope(w, Pending)
	w.ActiveRepairID = ""
	s.cancelWork(actor, w, reason)
}

// cancelWork records the cancellation of one work item and emits it.
func (s *Store) cancelWork(actor identity.ActorID, w Work, reason string) {
	w.State, w.Blocker, w.Note = Cancelled, "", reason
	w.Revision++
	s.putWork(w.ID, w)
	s.emit(WorkCancelled, actor, w, true)
}

// Cancelling repair cancels its implementation cycle. Cancelling an audit alone
// returns the unchanged submission to needs_check so another auditor can review it.
func (s *Store) Cancel(actor identity.ActorID, r CancelRequest) (result Work, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
	defer s.endMutation(&err)
	w, err := s.target(actor, r.WorkTarget, true)
	if err != nil {
		return Work{}, err
	}
	if !live(w) || blank(r.Reason) {
		return Work{}, ErrState
	}
	switch w.Kind {
	case Implementation:
		s.cancelImplementation(actor, w, r.Reason)
	case Repair:
		s.cancelImplementation(actor, s.works[w.ParentID], r.Reason)
	case Review, WebResearch, DeepResearch, Experiment:
		s.settleScope(w, Pending)
		s.cancelWork(actor, w, r.Reason)
	case AuditWork:
		s.cancelWork(actor, w, r.Reason)
		original := s.works[w.ParentID]
		original.State = NeedsCheck
		original.Revision++
		s.putWork(original.ID, original)
		s.emit(ReviewRequested, actor, original, true)
	default:
		return Work{}, invalid("unknown work kind")
	}
	return s.works[w.ID].Clone(), nil
}
