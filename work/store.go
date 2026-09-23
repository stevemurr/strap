package work

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/stevemurr/strap/identity"
)

// Store owns one conversation's work. It never calls external code under its lock.
// Creation is not deduplicated; state/revision checks fence repeated transitions.
type Store struct {
	evidenceLookup   EvidenceLookup
	emission         sync.Mutex
	reporter         Reporter
	change           Change
	visibleEvents    int
	failure          error
	mu               sync.Mutex
	issued           map[string]bool
	plans            map[PlanID]Plan
	works            map[ID]Work
	submissions      map[SubmissionID]Submission
	audits           map[AuditID]Audit
	progressReports  map[ProgressReportID]WorkProgressReport
	researchBriefs   map[ResearchBriefID]ResearchBrief
	progressFindings map[ProgressFindingID]ProgressFinding
	reserved         map[StepID]ID
	events           []Event
	ready            chan struct{}
}

func New(options ...Option) *Store {
	s := &Store{plans: map[PlanID]Plan{}, works: map[ID]Work{}, submissions: map[SubmissionID]Submission{}, audits: map[AuditID]Audit{}, reserved: map[StepID]ID{}, ready: make(chan struct{}, 1)}
	s.progressReports = map[ProgressReportID]WorkProgressReport{}
	s.researchBriefs = map[ResearchBriefID]ResearchBrief{}
	s.progressFindings = map[ProgressFindingID]ProgressFinding{}
	for _, o := range options {
		o(s)
	}
	return s
}

// id issues an opaque identifier: a kind prefix and a random base36 suffix.
// Ids are deliberately not sequential. A model that sees brief-9 must not be
// able to extrapolate the next id; it learns ids only from receipts, notices
// and reads. Uniqueness is checked against every id this store has issued.
func (s *Store) id(prefix string) string {
	if s.issued == nil {
		s.issued = map[string]bool{}
	}
	for {
		suffix := strconv.FormatUint(rand.Uint64()%idSpace, 36)
		for len(suffix) < idLength {
			suffix = "0" + suffix
		}
		candidate := prefix + "-" + suffix
		if !s.issued[candidate] {
			s.issued[candidate] = true
			return candidate
		}
	}
}

// idLength base36 characters give 36^7 (78 billion) ids per kind.
const idLength = 7
const idSpace = 36 * 36 * 36 * 36 * 36 * 36 * 36

func blank(v string) bool      { return strings.TrimSpace(v) == "" }
func invalid(why string) error { return fmt.Errorf("%w: %s", ErrInvalid, why) }
func live(w Work) bool         { return !w.State.Terminal() }
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
	for i, e := range s.events {
		if e.ID == id {
			if i < s.visibleEvents {
				s.visibleEvents--
			}
			s.events = append(s.events[:i], s.events[i+1:]...)
			break
		}
	}
	return nil
}

// knownWorks, knownPlans and knownBriefs name what an actor could have meant
// when an id is not found. Callers hold s.mu. Lists are sorted and bounded so
// a rejection stays short; a model that guessed an id gets the real ones back.
func (s *Store) knownWorks(actor identity.ActorID) string {
	var parts []string
	for _, w := range s.works {
		if w.visibleTo(actor) && live(w) {
			parts = append(parts, fmt.Sprintf("%s (%s, %s, revision %d)", w.ID, w.Kind, w.State, w.Revision))
		}
	}
	return known("live work visible to you", "no live work is visible to you", parts)
}
func (s *Store) knownPlans(actor identity.ActorID) string {
	var parts []string
	for _, p := range s.plans {
		if actor != "" && p.Owner == actor {
			parts = append(parts, fmt.Sprintf("%s (revision %d)", p.ID, p.Revision))
		}
	}
	return known("plans you own", "you own no plan", parts)
}
func (s *Store) knownBriefs(actor identity.ActorID) string {
	var delivered, pending []string
	for _, b := range s.researchBriefs {
		if w, ok := s.works[b.WorkID]; ok && w.visibleTo(actor) {
			delivered = append(delivered, fmt.Sprintf("%s (%s)", b.ID, w.ID))
		}
	}
	for _, w := range s.works {
		if w.Kind == Research && live(w) && w.LatestResearchBriefID == "" && w.visibleTo(actor) {
			pending = append(pending, string(w.ID))
		}
	}
	out := known("delivered briefs", "no brief has been delivered to you", delivered)
	if len(pending) > 0 {
		out += "; " + known("research not yet delivered", "", pending)
	}
	return out
}
func known(label, none string, parts []string) string {
	if len(parts) == 0 {
		return none
	}
	slices.Sort(parts)
	if len(parts) > 8 {
		parts = append(parts[:8], fmt.Sprintf("and %d more", len(parts)-8))
	}
	return label + ": " + strings.Join(parts, ", ")
}

// assigned resolves the work an actor may act on. It carries no optimistic
// concurrency, so it suits operations that only add to a record the actor alone
// writes; anything that transitions shared state goes through target.
func (s *Store) assigned(actor identity.ActorID, id ID, owner bool) (Work, error) {
	w, ok := s.works[id]
	if !ok {
		return Work{}, s.missingWork(actor, id)
	}
	allowed := w.Assignee
	if owner {
		allowed = w.Owner
	}
	if actor == "" || actor != allowed {
		return Work{}, ErrForbidden
	}
	return w.Clone(), nil
}

func (s *Store) target(actor identity.ActorID, t WorkTarget, owner bool) (Work, error) {
	w, err := s.assigned(actor, t.ID, owner)
	if err != nil {
		return Work{}, err
	}
	if t.ExpectedRevision == 0 || w.Revision != t.ExpectedRevision {
		return Work{}, fmt.Errorf("%w: %s is at revision %d but expected_revision was %d; use the work_revision from your last receipt or read get_work", ErrConflict, w.ID, w.Revision, t.ExpectedRevision)
	}
	return w, nil
}
func (s *Store) UpdatePlan(actor identity.ActorID, u PlanUpdate) (result Plan, err error) {
	if err = s.beginMutation(); err != nil {
		return result, err
	}
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
			return Plan{}, fmt.Errorf("%w: plan %s; %s", ErrNotFound, *u.PlanID, s.knownPlans(actor))
		}
		if p.Owner != actor {
			return Plan{}, ErrForbidden
		}
		if u.ExpectedRevision == nil || *u.ExpectedRevision != p.Revision {
			return Plan{}, fmt.Errorf("%w: plan %s is at revision %d; use the revision from get_plan as expected_revision", ErrConflict, p.ID, p.Revision)
		}
		p = p.Clone()
	}
	if u.Title != nil {
		if blank(*u.Title) {
			return Plan{}, invalid("empty title")
		}
		if strings.TrimSpace(*u.Title) == strings.TrimSpace(p.Title) {
			return Plan{}, fmt.Errorf("%w: plan %s already has title %q; nothing to change", ErrInvalid, p.ID, p.Title)
		}
		p.Title = *u.Title
	}
	seen := map[StepID]bool{}
	for _, edit := range u.Steps {
		if edit.ID == nil {
			if edit.Title == nil || blank(*edit.Title) {
				return Plan{}, invalid("new step requires title")
			}
			// A new step whose title matches a live step is almost always a
			// snapshot copied back instead of an edit; name the existing step.
			if u.PlanID != nil {
				title := strings.TrimSpace(*edit.Title)
				for _, existing := range p.Steps {
					if existing.Status != CancelledStep && strings.TrimSpace(existing.Title) == title {
						return Plan{}, invalid(fmt.Sprintf("step %q already exists as %s; use edit_step with that step_id, or choose a distinct title", title, existing.ID))
					}
				}
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
			return Plan{}, fmt.Errorf("%w: step %s is not in plan %s", ErrNotFound, *edit.ID, p.ID)
		}
		if by := s.reserved[*edit.ID]; by != "" {
			return Plan{}, fmt.Errorf("%w: step %s is reserved by %s; omit reserved steps from edits", ErrReserved, *edit.ID, by)
		}
		if p.Steps[i].Status == Completed {
			return Plan{}, fmt.Errorf("%w: step %s is completed; omit completed steps from edits", ErrState, *edit.ID)
		}
		// A model that re-sends a step's current values reads the new revision
		// as progress and sends them again; one session did so 155 times. An
		// edit that changes nothing is an error and consumes no revision.
		changed := false
		if edit.Title != nil {
			if blank(*edit.Title) {
				return Plan{}, invalid("empty step title")
			}
			changed = changed || strings.TrimSpace(*edit.Title) != strings.TrimSpace(p.Steps[i].Title)
			p.Steps[i].Title = *edit.Title
		}
		if edit.AcceptanceCriteria != nil {
			changed = changed || !slices.Equal(*edit.AcceptanceCriteria, p.Steps[i].AcceptanceCriteria)
			p.Steps[i].AcceptanceCriteria = slices.Clone(*edit.AcceptanceCriteria)
		}
		if !changed {
			// Roots reach for edit_step to close a step no work ever covered;
			// say how steps actually complete.
			return Plan{}, fmt.Errorf("%w: step %s already has that title and acceptance criteria; nothing to change. edit_step only rewords a step and cannot mark it done: a step completes when an audit accepts implementation work whose scope includes it. This step is %s; to finish it, assign it with assign_implementation, or remove it with cancel_steps if it is no longer needed", ErrInvalid, *edit.ID, p.Steps[i].Status)
		}
	}
	for _, id := range u.Cancel {
		if id == "" || seen[id] {
			return Plan{}, invalid("invalid or repeated step ID")
		}
		seen[id] = true
		i := slices.IndexFunc(p.Steps, func(v Step) bool { return v.ID == id })
		if i < 0 {
			return Plan{}, fmt.Errorf("%w: step %s is not in plan %s", ErrNotFound, id, p.ID)
		}
		if by := s.reserved[id]; by != "" {
			return Plan{}, fmt.Errorf("%w: step %s is reserved by %s and cannot be cancelled", ErrReserved, id, by)
		}
		if p.Steps[i].Status == Completed {
			return Plan{}, fmt.Errorf("%w: step %s is completed and cannot be cancelled", ErrState, id)
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
		if slices.EqualFunc(ordered, p.Steps, func(a, b Step) bool { return a.ID == b.ID }) {
			return Plan{}, fmt.Errorf("%w: steps are already in that order; nothing to change", ErrInvalid)
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
	if err = s.beginMutation(); err != nil {
		return result, err
	}
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
				return Work{}, fmt.Errorf("%w: step %s is not in plan %s", ErrNotFound, id, p.ID)
			}
			if by := s.reserved[id]; by != "" {
				return Work{}, fmt.Errorf("%w: step %s is reserved by %s; scope only available steps", ErrReserved, id, by)
			}
			if p.Steps[i].Status == Completed || p.Steps[i].Status == CancelledStep {
				return Work{}, fmt.Errorf("%w: step %s is %s and cannot be assigned", ErrState, id, p.Steps[i].Status)
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

func (s *Store) GetWork(actor identity.ActorID, id ID) (Work, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.works[id]
	if !ok {
		return Work{}, s.missingWork(actor, id)
	}
	if !w.visibleTo(actor) {
		return Work{}, ErrForbidden
	}
	return w.Clone(), nil
}
func (s *Store) GetPlan(actor identity.ActorID, id PlanID) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return Plan{}, fmt.Errorf("%w: plan %s; %s", ErrNotFound, id, s.knownPlans(actor))
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

// repairSource matches the immutable input, never the latest parent submission.
func (s *Store) repairSource(w Work, actor identity.ActorID, sub SubmissionID) bool {
	return actor != "" && w.Kind == Repair && w.Assignee == actor && w.State != Cancelled && w.RequestedByAuditID != "" && s.audits[w.RequestedByAuditID].SubmissionID == sub
}
func (s *Store) canReadSubmissionIndependently(actor identity.ActorID, sub Submission) bool {
	if actor == "" {
		return false
	}
	w := s.works[sub.WorkID]
	if w.Owner == actor || w.Assignee == actor || sub.SubmittedBy == actor {
		return true
	}
	for _, child := range s.works {
		if child.Kind == AuditWork && child.Assignee == actor && child.State != Cancelled && child.SubjectSubmissionID == sub.ID {
			return true
		}
	}
	return false
}
func (s *Store) canReadSubmission(actor identity.ActorID, sub Submission) bool {
	if s.canReadSubmissionIndependently(actor, sub) {
		return true
	}
	for _, child := range s.works {
		if s.repairSource(child, actor, sub.ID) {
			return true
		}
	}
	return false
}

// Source reads filter by the repair scope, not the source submitter's wider scope.
func (s *Store) submissionView(actor identity.ActorID, sub Submission) Submission {
	view := sub.Clone()
	original := s.works[sub.WorkID]
	if actor == original.Owner || actor == original.Assignee {
		return view
	}
	allowed := map[StepID]bool{}
	for _, child := range s.works {
		if child.Kind == AuditWork && child.Assignee == actor && child.State != Cancelled && child.SubjectSubmissionID == sub.ID {
			return view
		}
		if s.repairSource(child, actor, sub.ID) && child.Scope != nil {
			for _, id := range child.Scope.StepIDs {
				allowed[id] = true
			}
		}
	}
	if sub.SubmittedBy == actor {
		if via := s.works[sub.SubmittedVia]; via.Scope != nil {
			for _, id := range via.Scope.StepIDs {
				allowed[id] = true
			}
		}
	}
	view.Steps = slices.DeleteFunc(view.Steps, func(step Step) bool { return !allowed[step.ID] })
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
	if s.canReadSubmissionIndependently(actor, s.submissions[a.SubmissionID]) {
		return a.Clone(), nil
	}
	for _, child := range s.works {
		if child.RequestedByAuditID == a.ID && s.repairSource(child, actor, a.SubmissionID) {
			return a.Clone(), nil
		}
	}
	return Audit{}, ErrForbidden
}
