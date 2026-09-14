package work

import (
	"errors"
	"github.com/stevemurr/strap/identity"
	"reflect"
	"sync"
	"testing"
)

func ptr[T any](v T) *T        { return &v }
func target(w Work) WorkTarget { return WorkTarget{ID: w.ID, ExpectedRevision: w.Revision} }
func fixture(t *testing.T) (*Store, Plan, Work) {
	t.Helper()
	s := New()
	p, e := s.UpdatePlan("root", PlanUpdate{Title: ptr("storage"), Steps: []StepEdit{{Title: ptr("write"), AcceptanceCriteria: ptr([]string{"errors are propagated"})}, {Title: ptr("test")}, {Title: ptr("integrate")}}})
	if e != nil {
		t.Fatal(e)
	}
	w, e := s.AssignWork("root", AssignRequest{Assignee: "impl", Task: "implement storage", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[0].ID, p.Steps[1].ID}}})
	if e != nil {
		t.Fatal(e)
	}
	return s, p, w
}
func ready(t *testing.T, s *Store, w Work) Work {
	t.Helper()
	var changes []StepProgress
	if w.Scope != nil {
		for _, id := range w.Scope.StepIDs {
			changes = append(changes, StepProgress{ID: id, Status: ptr(ReadyForReview)})
		}
	}
	w, e := s.reportSnapshot(w.Assignee, ReportWorkProgressRequest{WorkTarget: target(w), Steps: changes, AssignedAtRevision: w.AssignedAtRevision})
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func submit(t *testing.T, s *Store, w Work) Submission {
	t.Helper()
	sub, e := s.SubmitWork(w.Assignee, SubmitRequest{WorkTarget: target(w), Summary: "implemented", Evidence: []string{"checks passed"}})
	if e != nil {
		t.Fatal(e)
	}
	return sub
}
func review(t *testing.T, s *Store, id ID, sub SubmissionID) Work {
	t.Helper()
	w, e := s.GetWork("root", id)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.AssignAudit("root", AssignAuditRequest{WorkTarget: target(w), SubmissionID: sub, Auditor: "reviewer"})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func TestAuditRepairAcceptance(t *testing.T) {
	s, p, w := fixture(t)
	w = ready(t, s, w)
	sub := submit(t, s, w)
	if _, e := s.reportSnapshot("impl", ReportWorkProgressRequest{WorkTarget: target(w), AssignedAtRevision: w.AssignedAtRevision, Position: &WorkPosition{Objective: "fixture progress", Note: *ptr("late")}}); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale update: %v", e)
	}
	current, _ := s.GetWork("root", w.ID)
	if _, e := s.reportSnapshot("impl", ReportWorkProgressRequest{WorkTarget: target(current), AssignedAtRevision: current.AssignedAtRevision, Position: &WorkPosition{Objective: "fixture progress", Note: *ptr("late")}}); !errors.Is(e, ErrState) {
		t.Fatalf("submitted update: %v", e)
	}
	a := review(t, s, w.ID, sub.ID)
	a, e := s.reportSnapshot("reviewer", ReportWorkProgressRequest{WorkTarget: target(a), AssignedAtRevision: a.AssignedAtRevision, Position: &WorkPosition{Objective: "fixture progress", Blocker: *ptr("environment unavailable")}})
	if e != nil {
		t.Fatal(e)
	}
	original, _ := s.GetWork("root", w.ID)
	if original.State != Checking || a.State != Active {
		t.Fatal("blocker changed lifecycle")
	}
	if _, e = s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Pass, Summary: "ok"}); e == nil {
		t.Fatal("accepted blocked audit")
	}
	events := s.PendingEvents(0)
	if !events[len(events)-1].Actionable {
		t.Fatal("blocker did not notify owner")
	}
	a, e = s.reportSnapshot("reviewer", ReportWorkProgressRequest{WorkTarget: target(a), AssignedAtRevision: a.AssignedAtRevision, Position: &WorkPosition{Objective: "fixture progress", Blocker: *ptr("")}})
	if e != nil {
		t.Fatal(e)
	}
	request := AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Fail, Summary: "write errors missing", Findings: []Finding{{StepIDs: []StepID{p.Steps[0].ID}, Description: "errors ignored", RequiredChange: "propagate errors", Verification: "failure test"}}}
	outcome, e := s.SubmitAudit("reviewer", request)
	if e != nil {
		t.Fatal(e)
	}
	n := len(s.PendingEvents(0))
	if _, e = s.SubmitAudit("reviewer", request); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if len(s.PendingEvents(0)) != n {
		t.Fatal("duplicate verdict had side effects")
	}
	repair, e := assignRepairForTest(t, s, outcome)
	if e != nil {
		t.Fatal(e)
	}
	if repair.RequestedBy != "root" || repair.Owner != "root" || repair.Assignee != "impl" || !reflect.DeepEqual(repair.Scope.StepIDs, []StepID{p.Steps[0].ID}) {
		t.Fatalf("bad repair: %+v", repair)
	}
	if _, e = s.reportSnapshot("impl", ReportWorkProgressRequest{WorkTarget: target(repair), Steps: []StepProgress{{ID: p.Steps[1].ID, Status: ptr(InProgress)}}, AssignedAtRevision: repair.AssignedAtRevision}); !errors.Is(e, ErrForbidden) {
		t.Fatal("repair expanded scope", e)
	}
	plan, _ := s.GetPlan("root", p.ID)
	if plan.Steps[0].Status != Pending || plan.Steps[1].Status != ReadyForReview {
		t.Fatal(plan)
	}
	repair = ready(t, s, repair)
	replacement := submit(t, s, repair)
	if replacement.Supersedes != sub.ID || len(replacement.Steps) != 2 {
		t.Fatal(replacement)
	}
	a2 := review(t, s, w.ID, replacement.ID)
	if _, e = s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a2), SubmissionID: sub.ID, Verdict: Pass, Summary: "stale"}); !errors.Is(e, ErrState) {
		t.Fatal(e)
	}
	_, e = s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a2), SubmissionID: replacement.ID, Verdict: Pass, Summary: "verified"})
	if e != nil {
		t.Fatal(e)
	}
	plan, _ = s.GetPlan("root", p.ID)
	original, _ = s.GetWork("root", w.ID)
	if original.State != Accepted || plan.Steps[0].Status != Completed || plan.Steps[1].Status != Completed || plan.Steps[2].Status != Pending {
		t.Fatal(original, plan)
	}
	old, _ := s.GetSubmission("root", sub.ID)
	if old.Summary != "implemented" || old.Steps[0].Status != ReadyForReview {
		t.Fatal("submission mutated")
	}
}
func TestPlanScopeAndAtomicity(t *testing.T) {
	s, p, w := fixture(t)
	missing := PlanID("missing")
	if _, e := s.UpdatePlan("root", PlanUpdate{PlanID: &missing, ExpectedRevision: ptr(Revision(1)), Title: ptr("typo")}); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	for _, u := range []PlanUpdate{{PlanID: ptr(PlanID(""))}, {Title: ptr("x"), Steps: []StepEdit{{ID: ptr(StepID("x")), Title: ptr("x")}}}} {
		if _, e := s.UpdatePlan("root", u); e == nil {
			t.Fatal("invalid create accepted")
		}
	}
	if _, e := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Title: ptr("should roll back"), Steps: []StepEdit{{ID: &p.Steps[0].ID, Title: ptr("change reserved")}}}); !errors.Is(e, ErrReserved) {
		t.Fatal(e)
	}
	got, _ := s.GetPlan("root", p.ID)
	if got.Title != p.Title {
		t.Fatal("partial plan edit")
	}
	if _, e := s.AssignWork("root", AssignRequest{Task: "overlap", Assignee: "other", Scope: w.Scope}); !errors.Is(e, ErrReserved) {
		t.Fatal(e)
	}
	got, _ = s.GetPlan("impl", p.ID)
	if len(got.Steps) != 2 {
		t.Fatal("scope read leaked")
	}
	if _, e := s.GetPlan("stranger", p.ID); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := s.reportSnapshot("impl", ReportWorkProgressRequest{WorkTarget: target(w), Steps: []StepProgress{{ID: p.Steps[0].ID, Status: ptr(InProgress)}, {ID: p.Steps[2].ID, Status: ptr(ReadyForReview)}}, AssignedAtRevision: w.AssignedAtRevision}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	got, _ = s.GetPlan("root", p.ID)
	if got.Steps[0].Status != Pending {
		t.Fatal("partial progress")
	}
	if _, e := s.reportSnapshot("impl", ReportWorkProgressRequest{WorkTarget: target(w), Steps: []StepProgress{{ID: p.Steps[0].ID, Status: ptr(Completed)}}, AssignedAtRevision: w.AssignedAtRevision}); e == nil {
		t.Fatal("self acceptance")
	}
	w = ready(t, s, w)
	got, e := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Title: ptr("new title")})
	if e != nil || got.Title != "new title" {
		t.Fatal("progress invalidated structural revision", e)
	}
}
func TestReassignmentCancellationAndSnapshots(t *testing.T) {
	s, p, w := fixture(t)
	originalScope := w.Scope.StepIDs[0]
	w.Scope.StepIDs[0] = "mutated"
	snapshot, _ := s.GetWork("root", w.ID)
	if snapshot.Scope.StepIDs[0] != originalScope {
		t.Fatal("snapshot alias")
	}
	w = snapshot
	old := target(w)
	replacement, e := s.Reassign("root", ReassignRequest{WorkTarget: old, Assignee: "replacement"})
	if e != nil {
		t.Fatal(e)
	}
	if replacement.AssignedAtRevision != replacement.Revision {
		t.Fatal(replacement)
	}
	if _, e = s.reportSnapshot("impl", ReportWorkProgressRequest{WorkTarget: old, AssignedAtRevision: 1, Position: &WorkPosition{Objective: "fixture progress", Note: *ptr("old")}}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = s.reportSnapshot("replacement", ReportWorkProgressRequest{WorkTarget: old, AssignedAtRevision: 1, Position: &WorkPosition{Objective: "fixture progress", Note: *ptr("old")}}); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	replacement = ready(t, s, replacement)
	binding := replacement.AssignedAtRevision
	if replacement.Revision == binding {
		t.Fatal("progress did not advance revision")
	}
	sub := submit(t, s, replacement)
	sub.Steps[0].AcceptanceCriteria[0] = "mutation"
	sub.Evidence[0] = "mutation"
	stored, _ := s.GetSubmission("root", sub.ID)
	if stored.Evidence[0] == "mutation" || stored.Steps[0].AcceptanceCriteria[0] == "mutation" {
		t.Fatal("submission alias")
	}
	a := review(t, s, w.ID, sub.ID)
	if _, e = s.Reassign("root", ReassignRequest{WorkTarget: target(a), Assignee: "replacement"}); !errors.Is(e, ErrForbidden) {
		t.Fatal("self-review allowed", e)
	}
	a, e = s.Cancel("root", CancelRequest{WorkTarget: target(a), Reason: "different reviewer needed"})
	if e != nil || a.State != Cancelled {
		t.Fatal(e)
	}
	current, _ := s.GetWork("root", w.ID)
	if current.State != NeedsCheck {
		t.Fatal(current)
	}
	a = review(t, s, w.ID, sub.ID)
	current, _ = s.GetWork("root", w.ID)
	if _, e = s.Cancel("root", CancelRequest{WorkTarget: target(current), Reason: "scope changed"}); e != nil {
		t.Fatal(e)
	}
	a, _ = s.GetWork("root", a.ID)
	if a.State != Cancelled {
		t.Fatal("audit survived cancellation")
	}
	if _, e = s.AssignWork("root", AssignRequest{Task: "retry", Assignee: "next", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{originalScope}}}); e != nil {
		t.Fatal("reservation retained", e)
	}
	events := s.PendingEvents(0)
	events[0].Plan.Steps[0].Title = "mutation"
	again := s.PendingEvents(0)
	if again[0].Plan.Steps[0].Title == "mutation" {
		t.Fatal("event alias")
	}
}
func TestConcurrentDisjointProgress(t *testing.T) {
	s, p, w := fixture(t)
	other, e := s.AssignWork("root", AssignRequest{Task: "integrate", Assignee: "other", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[2].ID}}})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, item := range []Work{w, other} {
		wg.Add(1)
		go func(w Work) {
			defer wg.Done()
			_, e := s.reportSnapshot(w.Assignee, ReportWorkProgressRequest{WorkTarget: target(w), Steps: []StepProgress{{ID: w.Scope.StepIDs[0], Status: ptr(InProgress)}}, AssignedAtRevision: w.AssignedAtRevision})
			errs <- e
		}(item)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	plan, _ := s.GetPlan("root", p.ID)
	if plan.Steps[0].Status != InProgress || plan.Steps[2].Status != InProgress {
		t.Fatal(plan)
	}
}
func TestStandaloneAuditAndRepairCancellation(t *testing.T) {
	s := New()
	w, e := s.AssignWork("root", AssignRequest{Assignee: "impl", Task: "write report"})
	if e != nil {
		t.Fatal(e)
	}
	sub := submit(t, s, w)
	a := review(t, s, w.ID, sub.ID)
	outcome, e := s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Fail, Summary: "missing source", Findings: []Finding{{Description: "missing source", RequiredChange: "add source", Verification: "source supports claim"}}})
	if e != nil {
		t.Fatal(e)
	}
	r, _ := assignRepairForTest(t, s, outcome)
	if _, e = s.Cancel("root", CancelRequest{WorkTarget: target(r), Reason: "withdraw task"}); e != nil {
		t.Fatal(e)
	}
	original, _ := s.GetWork("root", w.ID)
	if original.State != Cancelled {
		t.Fatal("orphaned repair cycle")
	}
}

func TestRepairReassignmentKeepsReadAndWriteScope(t *testing.T) {
	s, p, w := fixture(t)
	w = ready(t, s, w)
	sub := submit(t, s, w)
	a := review(t, s, w.ID, sub.ID)
	outcome, e := s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Fail, Summary: "fix one step", Findings: []Finding{{StepIDs: []StepID{p.Steps[0].ID}, Description: "bad write", RequiredChange: "fix write", Verification: "test write"}}})
	if e != nil {
		t.Fatal(e)
	}
	repair, _ := assignRepairForTest(t, s, outcome)
	repair, e = s.Reassign("root", ReassignRequest{WorkTarget: target(repair), Assignee: "replacement"})
	if e != nil {
		t.Fatal(e)
	}
	original, _ := s.GetWork("root", w.ID)
	if original.Assignee != "impl" {
		t.Fatal("repair silently reassigned original")
	}
	if _, e = s.GetWork("replacement", w.ID); !errors.Is(e, ErrForbidden) {
		t.Fatal("repair actor can read parent", e)
	}
	plan, e := s.GetPlan("replacement", p.ID)
	if e != nil || len(plan.Steps) != 1 {
		t.Fatal("repair scope broadened", plan, e)
	}
	repair = ready(t, s, repair)
	view := submit(t, s, repair)
	if len(view.Steps) != 1 || view.Steps[0].ID != p.Steps[0].ID {
		t.Fatal("submission result leaked steps", view)
	}
	view, e = s.GetSubmission("replacement", view.ID)
	if e != nil || len(view.Steps) != 1 {
		t.Fatal("submission read leaked steps", e)
	}
	full, _ := s.GetSubmission("root", view.ID)
	if len(full.Steps) != 2 {
		t.Fatal("canonical submission narrowed")
	}
	a = review(t, s, w.ID, view.ID)
	next, e := s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a), SubmissionID: view.ID, Verdict: Fail, Summary: "still needs repair", Findings: []Finding{{StepIDs: []StepID{p.Steps[0].ID}, Description: "still bad", RequiredChange: "fix", Verification: "test"}}})
	if e != nil {
		t.Fatal(e)
	}
	nextRepair, _ := assignRepairForTest(t, s, next)
	if nextRepair.Assignee != "replacement" {
		t.Fatal("repair returned to obsolete implementor", nextRepair)
	}
}

func assignRepairForTest(t *testing.T, s *Store, a Audit) (Work, error) {
	t.Helper()
	aw, e := s.GetWork("root", a.WorkID)
	if e != nil {
		return Work{}, e
	}
	original, e := s.GetWork("root", aw.ParentID)
	if e != nil {
		return Work{}, e
	}
	return s.AssignRepair("root", AssignRepairRequest{WorkTarget: target(original), Assignee: func() identity.ActorID { sub, _ := s.GetSubmission("root", a.SubmissionID); return sub.SubmittedBy }(), AuditID: a.ID})
}
