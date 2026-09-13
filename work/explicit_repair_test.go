package work

import (
	"errors"
	"github.com/stevemurr/strap/identity"
	"reflect"
	"sync"
	"testing"
)

func failingSubmission(t *testing.T) (*Store, Plan, Work, Submission, Audit) {
	t.Helper()
	s, p, w := fixture(t)
	// Preserve task context for a fresh repair assignee.
	s.mu.Lock()
	original := s.works[w.ID]
	original.Context = "original background"
	original.ExpectedOutput = "working storage"
	s.works[w.ID] = original
	s.mu.Unlock()
	w = ready(t, s, w)
	sub, e := s.SubmitWork(w.Assignee, SubmitRequest{WorkTarget: target(w), Summary: "source outcome", Evidence: []string{"source test"}, Artifacts: []ArtifactRef{{URI: "file:result"}}})
	if e != nil {
		t.Fatal(e)
	}
	aw := review(t, s, w.ID, sub.ID)
	audit, e := s.SubmitAudit(aw.Assignee, AuditRequest{WorkTarget: target(aw), SubmissionID: sub.ID, Verdict: Fail, Summary: "fix first step", Findings: []Finding{{StepIDs: []StepID{p.Steps[0].ID}, Description: "ignored error", RequiredChange: "handle it", Verification: "failure test"}}})
	if e != nil {
		t.Fatal(e)
	}
	w, e = s.GetWork("root", w.ID)
	if e != nil {
		t.Fatal(e)
	}
	return s, p, w, sub, audit
}
func TestFailedAuditWaitsForExplicitRepairAndRejectsConcurrentDuplicates(t *testing.T) {
	s, _, w, sub, a := failingSubmission(t)
	if w.State != ChangesRequested || w.LatestAuditID != a.ID || w.ActiveRepairID != "" || a.RepairWorkID != "" {
		t.Fatal(w, a)
	}
	for _, candidate := range s.works {
		if candidate.Kind == Repair {
			t.Fatal("audit created repair")
		}
	}
	request := AssignRepairRequest{WorkTarget: target(w), Assignee: "fresh", AuditID: a.ID}
	var wg sync.WaitGroup
	out := make(chan Work, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := s.AssignRepair("root", request); out <- r; errs <- e }()
	}
	wg.Wait()
	close(out)
	close(errs)
	successes, conflicts := 0, 0
	for e := range errs {
		if e == nil {
			successes++
		} else if errors.Is(e, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	var repair Work
	for r := range out {
		if r.ID != "" {
			repair = r
		}
	}
	current, _ := s.GetWork("root", w.ID)
	if current.ActiveRepairID != repair.ID || current.Revision != w.Revision+1 {
		t.Fatal(current)
	}
	before := len(s.PendingEvents(0))
	request.WorkTarget = target(current)
	if _, e := s.AssignRepair("root", request); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if before != len(s.PendingEvents(0)) {
		t.Fatal("duplicate emitted work")
	}
	immutable, _ := s.GetAudit("root", a.ID)
	source, _ := s.GetSubmission("root", sub.ID)
	if !reflect.DeepEqual(immutable, a) || !reflect.DeepEqual(source, sub) {
		t.Fatal("repair mutated source")
	}
}
func TestFreshRepairContextAndDerivedReadAccess(t *testing.T) {
	s, p, w, sub, a := failingSubmission(t)
	repair, e := s.AssignRepair("root", AssignRepairRequest{WorkTarget: target(w), Assignee: "fresh", AuditID: a.ID})
	if e != nil {
		t.Fatal(e)
	}
	check := func(actor string) {
		t.Helper()
		in, e := s.InspectWork(identity.ActorID(actor), repair.ID)
		if e != nil || in.Audit == nil || in.Submission == nil {
			t.Fatal(in, e)
		}
		if in.Work.Context != "original background" || in.Submission.ID != sub.ID || in.Audit.ID != a.ID || len(in.Submission.Steps) != 1 || in.Submission.Steps[0].ID != p.Steps[0].ID || len(in.Submission.Artifacts) != 1 || in.Submission.Evidence[0] != "source test" {
			t.Fatal(in)
		}
	}
	check("fresh")
	check("root")
	other, e := s.AssignWork("root", AssignRequest{Assignee: "impl", Task: "unrelated"})
	if e != nil {
		t.Fatal(e)
	}
	unrelated := submit(t, s, other)
	if _, e = s.GetSubmission("fresh", unrelated.ID); !errors.Is(e, ErrForbidden) {
		t.Fatal("repair granted unrelated submission", e)
	}
	repair, e = s.Reassign("root", ReassignRequest{WorkTarget: target(repair), Assignee: "replacement"})
	if e != nil {
		t.Fatal(e)
	}
	for _, get := range []func() error{func() error { _, e := s.GetSubmission("fresh", sub.ID); return e }, func() error { _, e := s.GetAudit("fresh", a.ID); return e }} {
		if !errors.Is(get(), ErrForbidden) {
			t.Fatal("displaced repair retained derived access")
		}
	}
	check("replacement")
	// Passive replay follows the same visibility rules.
	view := NewReadModel()
	for _, event := range s.PendingEvents(0) {
		if event.Change != nil {
			view.Apply(*event.Change)
		}
	}
	got, e := view.InspectWork("replacement", repair.ID)
	live, _ := s.InspectWork("replacement", repair.ID)
	if e != nil || !reflect.DeepEqual(got, live) {
		t.Fatal("passive repair view differs", e)
	}
	if _, e = s.Cancel("root", CancelRequest{WorkTarget: target(repair), Reason: "withdraw"}); e != nil {
		t.Fatal(e)
	}
	current, _ := s.GetWork("root", w.ID)
	if current.State != Cancelled || current.ActiveRepairID != "" {
		t.Fatal(current)
	}
	if _, e = s.GetSubmission("replacement", sub.ID); !errors.Is(e, ErrForbidden) {
		t.Fatal("cancelled repair retained access", e)
	}
	if _, e = s.GetSubmission("impl", sub.ID); e != nil {
		t.Fatal("source submitter lost independent access", e)
	}
	for _, id := range w.Scope.StepIDs {
		if s.reserved[id] != "" {
			t.Fatal("cancel retained reservation")
		}
	}
}

func TestRepairSubmissionPinsSourceAndExcludesAllContributors(t *testing.T) {
	s, _, w, source, a := failingSubmission(t)
	repair, e := s.AssignRepair("root", AssignRepairRequest{WorkTarget: target(w), Assignee: "fresh", AuditID: a.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetWork("fresh", w.ID); !errors.Is(e, ErrForbidden) {
		t.Fatal("narrow repair can read original work", e)
	}
	repair = ready(t, s, repair)
	sub, e := s.SubmitWork("fresh", SubmitRequest{WorkTarget: target(repair), Summary: "repaired"})
	if e != nil {
		t.Fatal(e)
	}
	original, _ := s.GetWork("root", w.ID)
	closed, _ := s.GetWork("root", repair.ID)
	if original.ActiveRepairID != "" || original.State != NeedsCheck || closed.State != Closed || sub.Supersedes != source.ID || sub.SubmittedVia != repair.ID {
		t.Fatal(original, closed, sub)
	}
	in, e := s.InspectWork("fresh", repair.ID)
	if e != nil || in.Submission == nil || in.Submission.ID != source.ID || in.Audit.ID != a.ID {
		t.Fatal("repair drifted to latest submission", in, e)
	}
	full, e := s.GetSubmission("root", sub.ID)
	if e != nil || len(full.Steps) != 2 || len(sub.Steps) != 1 {
		t.Fatal(full, sub, e)
	}
	for _, actor := range []identity.ActorID{"impl", "fresh"} {
		if _, e = s.AssignAudit("root", AssignAuditRequest{WorkTarget: target(original), Auditor: actor, SubmissionID: sub.ID}); !errors.Is(e, ErrForbidden) {
			t.Fatal("contributor can audit", actor, e)
		}
	}
}

func TestRepairRejectsWrongAuthorityAndAuditWithoutMutation(t *testing.T) {
	s, _, w, _, a := failingSubmission(t)
	before := len(s.PendingEvents(0))
	tests := []struct {
		actor   identity.ActorID
		request AssignRepairRequest
		want    error
	}{
		{"other", AssignRepairRequest{WorkTarget: target(w), Assignee: "fresh", AuditID: a.ID}, ErrForbidden},
		{"root", AssignRepairRequest{WorkTarget: target(w), Assignee: "fresh", AuditID: "missing"}, ErrNotFound},
		{"root", AssignRepairRequest{WorkTarget: target(w), AuditID: a.ID}, ErrInvalid},
		{"root", AssignRepairRequest{WorkTarget: WorkTarget{ID: w.ID, ExpectedRevision: w.Revision - 1}, Assignee: "fresh", AuditID: a.ID}, ErrConflict},
	}
	for _, tc := range tests {
		if _, e := s.AssignRepair(tc.actor, tc.request); !errors.Is(e, tc.want) {
			t.Fatal(e, tc.want)
		}
		current, _ := s.GetWork("root", w.ID)
		if !reflect.DeepEqual(current, w) || len(s.PendingEvents(0)) != before {
			t.Fatal("invalid repair mutated ledger")
		}
	}
	if _, e := s.Cancel("root", CancelRequest{WorkTarget: target(w), Reason: "cancel before repair"}); e != nil {
		t.Fatal(e)
	}
	cancelled, _ := s.GetWork("root", w.ID)
	if _, e := s.AssignRepair("root", AssignRepairRequest{WorkTarget: target(cancelled), Assignee: "fresh", AuditID: a.ID}); !errors.Is(e, ErrState) {
		t.Fatal(e)
	}
}
