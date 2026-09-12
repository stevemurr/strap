package work

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/stevemurr/strap/identity"
)

// Store owns one conversation's work. It never calls external code under its lock.
// Creation is not deduplicated; state/revision checks fence repeated transitions.
type Store struct {
	emission      sync.Mutex
	reporter      Reporter
	change        Change
	visibleEvents int
	mu            sync.Mutex
	next          uint64
	plans         map[PlanID]Plan
	works         map[ID]Work
	submissions   map[SubmissionID]Submission
	audits        map[AuditID]Audit
	reserved      map[StepID]ID
	events        []Event
	ready         chan struct{}
}

func New(options ...Option) *Store {
	s := &Store{plans: map[PlanID]Plan{}, works: map[ID]Work{}, submissions: map[SubmissionID]Submission{}, audits: map[AuditID]Audit{}, reserved: map[StepID]ID{}, ready: make(chan struct{}, 1)}
	for _, o := range options {
		o(s)
	}
	return s
}
func (s *Store) id(prefix string) string { s.next++; return fmt.Sprintf("%s-%d", prefix, s.next) }
func blank(v string) bool                { return strings.TrimSpace(v) == "" }
func invalid(why string) error           { return fmt.Errorf("%w: %s", ErrInvalid, why) }
func live(w Work) bool                   { return w.State != Cancelled && w.State != Closed && w.State != Accepted }
func (s *Store) steps(scope *Scope) []Step {
	if scope == nil {
		return nil
	}
	var out []Step
	for _, step := range s.plans[scope.PlanID].Steps {
		if slices.Contains(scope.StepIDs, step.ID) {
			out = append(out, step)
		}
	}
	return cloneSteps(out)
}
func (s *Store) emit(kind EventKind, actor identity.ActorID, w Work, actionable bool) {
	s.events = append(s.events, Event{ID: EventID(s.id("event")), Kind: kind, Actor: actor, Work: w.Clone(), Steps: s.steps(w.Scope), SubmissionID: w.LatestSubmissionID, Actionable: actionable})

}

// Ready is a coalesced wakeup for the application's single event dispatcher.
func (s *Store) Ready() <-chan struct{} { return s.ready }
func (s *Store) PendingEvents(limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > s.visibleEvents {
		limit = s.visibleEvents
	}
	out := make([]Event, limit)
	for i := range out {
		out[i] = s.events[i].Clone()
	}
	return out
}
func (s *Store) AcknowledgeEvent(id EventID) error {
	s.emission.Lock()
	defer s.emission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = slices.DeleteFunc(s.events, func(event Event) bool { return event.ID == id })
	s.visibleEvents = len(s.events)
	return nil
}
func (s *Store) target(actor identity.ActorID, t WorkTarget, owner bool) (Work, error) {
	w, ok := s.works[t.ID]
	if !ok {
		return Work{}, ErrNotFound
	}
	allowed := w.Assignee
	if owner {
		allowed = w.Owner
	}
	if actor == "" || actor != allowed {
		return Work{}, ErrForbidden
	}
	if t.ExpectedRevision == 0 || w.Revision != t.ExpectedRevision {
		return Work{}, ErrConflict
	}
	return w.Clone(), nil
}
func (s *Store) UpdatePlan(actor identity.ActorID, u PlanUpdate) (result Plan, err error) {
	s.beginMutation()
	defer s.endMutation(&err)
	if blank(string(actor)) {
		return Plan{}, ErrForbidden
	}
	var p Plan
	if u.PlanID == nil {
		if u.ExpectedRevision != nil || u.Title == nil || blank(*u.Title) || len(u.Steps) == 0 || len(u.Order) > 0 || len(u.Cancel) > 0 {
			return Plan{}, invalid("creation requires title and new steps only")
		}
		p = Plan{ID: PlanID(s.id("plan")), Owner: actor}
	} else {
		if blank(string(*u.PlanID)) {
			return Plan{}, invalid("empty plan_id")
		}
		var ok bool
		p, ok = s.plans[*u.PlanID]
		if !ok {
			return Plan{}, ErrNotFound
		}
		if p.Owner != actor {
			return Plan{}, ErrForbidden
		}
		if u.ExpectedRevision == nil || *u.ExpectedRevision != p.Revision {
			return Plan{}, ErrConflict
		}
		p = p.Clone()
	}
	if u.Title != nil {
		if blank(*u.Title) {
			return Plan{}, invalid("empty title")
		}
		p.Title = *u.Title
	}
	seen := map[StepID]bool{}
	for _, edit := range u.Steps {
		if edit.ID == nil {
			if edit.Title == nil || blank(*edit.Title) {
				return Plan{}, invalid("new step requires title")
			}
			step := Step{ID: StepID(s.id("step")), Title: *edit.Title, Status: Pending}
			if edit.AcceptanceCriteria != nil {
				step.AcceptanceCriteria = slices.Clone(*edit.AcceptanceCriteria)
			}
			p.Steps = append(p.Steps, step)
			continue
		}
		if u.PlanID == nil || *edit.ID == "" || seen[*edit.ID] {
			return Plan{}, invalid("invalid or repeated step ID")
		}
		seen[*edit.ID] = true
		i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == *edit.ID })
		if i < 0 {
			return Plan{}, ErrNotFound
		}
		if s.reserved[*edit.ID] != "" {
			return Plan{}, ErrReserved
		}
		if p.Steps[i].Status == Completed {
			return Plan{}, ErrState
		}
		if edit.Title != nil {
			if blank(*edit.Title) {
				return Plan{}, invalid("empty step title")
			}
			p.Steps[i].Title = *edit.Title
		}
		if edit.AcceptanceCriteria != nil {
			p.Steps[i].AcceptanceCriteria = slices.Clone(*edit.AcceptanceCriteria)
		}
	}
	for _, id := range u.Cancel {
		if id == "" || seen[id] {
			return Plan{}, invalid("invalid or repeated step ID")
		}
		seen[id] = true
		i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == id })
		if i < 0 {
			return Plan{}, ErrNotFound
		}
		if s.reserved[id] != "" {
			return Plan{}, ErrReserved
		}
		if p.Steps[i].Status == Completed {
			return Plan{}, ErrState
		}
		p.Steps[i].Status = CancelledStep
	}
	if u.Order != nil {
		if len(u.Order) != len(p.Steps) {
			return Plan{}, invalid("order must contain every step exactly once")
		}
		ordered := make([]Step, 0, len(u.Order))
		seen = map[StepID]bool{}
		for _, id := range u.Order {
			i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == id })
			if i < 0 || seen[id] {
				return Plan{}, invalid("invalid order")
			}
			seen[id] = true
			ordered = append(ordered, p.Steps[i])
		}
		p.Steps = ordered
	}
	p.Revision++
	s.putPlan(p.ID, p.Clone())
	s.emit(PlanChanged, actor, Work{}, false)
	snapshot := p.Clone()
	s.events[len(s.events)-1].Plan = &snapshot
	return p.Clone(), nil
}
func (s *Store) AssignWork(actor identity.ActorID, r AssignRequest) (result Work, err error) {
	s.beginMutation()
	defer s.endMutation(&err)
	if blank(string(actor)) || blank(string(r.Assignee)) || blank(r.Task) {
		return Work{}, invalid("actor, assignee and task required")
	}
	if r.Scope != nil {
		p, ok := s.plans[r.Scope.PlanID]
		if !ok {
			return Work{}, ErrNotFound
		}
		if p.Owner != actor {
			return Work{}, ErrForbidden
		}
		if len(r.Scope.StepIDs) == 0 {
			return Work{}, invalid("scope must contain steps")
		}
		seen := map[StepID]bool{}
		for _, id := range r.Scope.StepIDs {
			if seen[id] {
				return Work{}, invalid("repeated scope step")
			}
			seen[id] = true
			i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == id })
			if i < 0 {
				return Work{}, ErrNotFound
			}
			if s.reserved[id] != "" {
				return Work{}, ErrReserved
			}
			if p.Steps[i].Status == Completed || p.Steps[i].Status == CancelledStep {
				return Work{}, ErrState
			}
		}
	}
	w := Work{ID: ID(s.id("work")), Kind: Implementation, State: Active, Revision: 1, AssignedAtRevision: 1, Owner: actor, RequestedBy: actor, Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope}.Clone()
	if w.Scope != nil {
		for _, id := range w.Scope.StepIDs {
			s.reserved[id] = w.ID
		}
	}
	s.putWork(w.ID, w)
	s.emit(WorkAssigned, actor, w, true)
	return w.Clone(), nil
}
func (s *Store) UpdateProgress(actor identity.ActorID, u ProgressUpdate) (result Work, err error) {
	s.beginMutation()
	defer s.endMutation(&err)
	w, err := s.target(actor, u.WorkTarget, false)
	if err != nil {
		return Work{}, err
	}
	if w.State != Active {
		return Work{}, ErrState
	}
	if len(u.Steps) > 0 && (w.Kind == AuditWork || w.Scope == nil) {
		return Work{}, ErrForbidden
	}
	var p Plan
	if w.Scope != nil {
		p = s.plans[w.Scope.PlanID].Clone()
	}
	seen := map[StepID]bool{}
	for _, change := range u.Steps {
		if seen[change.ID] {
			return Work{}, invalid("repeated progress step")
		}
		seen[change.ID] = true
		if !slices.Contains(w.Scope.StepIDs, change.ID) {
			return Work{}, ErrForbidden
		}
		if change.Status != nil && *change.Status != Pending && *change.Status != InProgress && *change.Status != Blocked && *change.Status != ReadyForReview {
			return Work{}, invalid("progress cannot accept or cancel work")
		}
		i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == change.ID })
		if i < 0 {
			return Work{}, ErrNotFound
		}
		if change.Status != nil {
			p.Steps[i].Status = *change.Status
		}
		if change.Note != nil {
			p.Steps[i].Note = *change.Note
		}
	}
	actionable := u.Blocker != nil && *u.Blocker != w.Blocker
	if u.Note != nil {
		w.Note = *u.Note
	}
	if u.Blocker != nil {
		w.Blocker = *u.Blocker
	}
	w.Revision++
	if w.Scope != nil {
		s.putPlan(p.ID, p)
	}
	s.putWork(w.ID, w)
	s.emit(ProgressChanged, actor, w, actionable)
	return w.Clone(), nil
}
func (s *Store) GetWork(actor identity.ActorID, id ID) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.works[id]
	if !ok {
		return Work{}, ErrNotFound
	}
	if actor == "" || (w.Owner != actor && w.Assignee != actor) {
		return Work{}, ErrForbidden
	}
	return w.Clone(), nil
}
func (s *Store) GetPlan(actor identity.ActorID, id PlanID) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return Plan{}, ErrNotFound
	}
	if actor == "" {
		return Plan{}, ErrForbidden
	}
	if p.Owner == actor {
		return p.Clone(), nil
	}
	allowed := map[StepID]bool{}
	for _, w := range s.works {
		if w.Assignee == actor && live(w) && w.Scope != nil && w.Scope.PlanID == id {
			for _, id := range w.Scope.StepIDs {
				allowed[id] = true
			}
		}
	}
	if len(allowed) == 0 {
		return Plan{}, ErrForbidden
	}
	p = p.Clone()
	p.Steps = slices.DeleteFunc(p.Steps, func(step Step) bool { return !allowed[step.ID] })
	return p, nil
}
func (s *Store) canReadSubmission(actor identity.ActorID, sub Submission) bool {
	w := s.works[sub.WorkID]
	if actor != "" && (w.Owner == actor || w.Assignee == actor || sub.SubmittedBy == actor) {
		return true
	}
	for _, audit := range s.works {
		if audit.Kind == AuditWork && audit.Assignee == actor && audit.State != Cancelled && audit.SubjectSubmissionID == sub.ID {
			return true
		}
	}
	return false
}

// A reassigned repair actor may submit a full-scope outcome, but only sees the
// step snapshots it was authorized to repair. The canonical submission stays full.
func (s *Store) submissionView(actor identity.ActorID, sub Submission) Submission {
	view := sub.Clone()
	original := s.works[sub.WorkID]
	if actor == original.Owner || actor == original.Assignee {
		return view
	}
	for _, w := range s.works {
		if w.Kind == AuditWork && w.Assignee == actor && w.State != Cancelled && w.SubjectSubmissionID == sub.ID {
			return view
		}
	}
	via := s.works[sub.SubmittedVia]
	if via.Scope != nil {
		view.Steps = slices.DeleteFunc(view.Steps, func(step Step) bool { return !slices.Contains(via.Scope.StepIDs, step.ID) })
	}
	return view
}
func (s *Store) GetSubmission(actor identity.ActorID, id SubmissionID) (Submission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.submissions[id]
	if !ok {
		return Submission{}, ErrNotFound
	}
	if !s.canReadSubmission(actor, sub) {
		return Submission{}, ErrForbidden
	}
	return s.submissionView(actor, sub), nil
}
func (s *Store) GetAudit(actor identity.ActorID, id AuditID) (Audit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.audits[id]
	if !ok {
		return Audit{}, ErrNotFound
	}
	if !s.canReadSubmission(actor, s.submissions[a.SubmissionID]) {
		r := s.works[a.RepairWorkID]
		if actor == "" || r.Assignee != actor {
			return Audit{}, ErrForbidden
		}
	}
	return a.Clone(), nil
}
