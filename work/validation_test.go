package work

import (
	"errors"
	"github.com/stevemurr/strap/identity"
	"reflect"
	"testing"
)

func TestPlanEditsReorderCancellationAndEventAcknowledgement(t *testing.T) {
	s := New()
	p, err := s.UpdatePlan("root", PlanUpdate{Title: ptr("plan"), Steps: []StepEdit{{Title: ptr("first")}, {Title: ptr("second")}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Ready():
	default:
		t.Fatal("no event wakeup")
	}
	events := s.PendingEvents(1)
	if len(events) != 1 || events[0].Plan.Title != "plan" {
		t.Fatal(events)
	}
	if err := s.AcknowledgeEvent(events[0].ID); err != nil {
		t.Fatal(err)
	}
	s.AcknowledgeEvent(events[0].ID)
	if len(s.PendingEvents(0)) != 0 {
		t.Fatal("acknowledged event retained")
	}
	first, second := p.Steps[0].ID, p.Steps[1].ID
	p, err = s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Steps: []StepEdit{{ID: &first, Title: ptr("renamed"), AcceptanceCriteria: ptr([]string{"verified"})}}, Cancel: []StepID{second}, Order: []StepID{second, first}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != 2 || p.Steps[0].ID != second || p.Steps[0].Status != CancelledStep || p.Steps[1].Title != "renamed" || !reflect.DeepEqual(p.Steps[1].AcceptanceCriteria, []string{"verified"}) {
		t.Fatal(p)
	}
	if _, err = s.AssignWork("root", AssignRequest{Assignee: "impl", Task: "x", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{second}}}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
}
func TestPlanInvalidEditsAreAtomic(t *testing.T) {
	for _, name := range []string{"actor", "owner", "revision missing", "revision stale", "empty title", "new step title", "unknown step", "empty step", "repeated step", "empty step title", "empty cancel", "unknown cancel", "repeated cancel", "short order", "unknown order", "repeated order"} {
		t.Run(name, func(t *testing.T) {
			s := New()
			p, err := s.UpdatePlan("root", PlanUpdate{Title: ptr("plan"), Steps: []StepEdit{{Title: ptr("one")}, {Title: ptr("two")}}})
			if err != nil {
				t.Fatal(err)
			}
			u := PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision}
			actor := identity.ActorID("root")
			want := ErrInvalid
			switch name {
			case "actor":
				actor = " "
				want = ErrForbidden
			case "owner":
				actor = "other"
				want = ErrForbidden
			case "revision missing":
				u.ExpectedRevision = nil
				want = ErrConflict
			case "revision stale":
				u.ExpectedRevision = ptr(Revision(99))
				want = ErrConflict
			case "empty title":
				u.Title = ptr(" ")
			case "new step title":
				u.Steps = []StepEdit{{}}
			case "unknown step":
				u.Steps = []StepEdit{{ID: ptr(StepID("missing"))}}
				want = ErrNotFound
			case "empty step":
				u.Steps = []StepEdit{{ID: ptr(StepID(""))}}
			case "repeated step":
				u.Steps = []StepEdit{{ID: &p.Steps[0].ID}, {ID: &p.Steps[0].ID}}
			case "empty step title":
				u.Steps = []StepEdit{{ID: &p.Steps[0].ID, Title: ptr(" ")}}
			case "empty cancel":
				u.Cancel = []StepID{""}
			case "unknown cancel":
				u.Cancel = []StepID{"missing"}
				want = ErrNotFound
			case "repeated cancel":
				u.Cancel = []StepID{p.Steps[0].ID, p.Steps[0].ID}
			case "short order":
				u.Order = []StepID{}
			case "unknown order":
				u.Order = []StepID{p.Steps[0].ID, "missing"}
			case "repeated order":
				u.Order = []StepID{p.Steps[0].ID, p.Steps[0].ID}
			}
			before := len(s.PendingEvents(0))
			if _, err := s.UpdatePlan(actor, u); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			got, _ := s.GetPlan("root", p.ID)
			if !reflect.DeepEqual(got, p) || len(s.PendingEvents(0)) != before {
				t.Fatal("rejected edit changed plan/events")
			}
		})
	}
}
func TestAssignmentValidation(t *testing.T) {
	for _, name := range []string{"empty actor", "empty assignee", "empty task", "unknown plan", "wrong owner", "empty scope", "repeated scope", "unknown step"} {
		t.Run(name, func(t *testing.T) {
			s := New()
			p, _ := s.UpdatePlan("root", PlanUpdate{Title: ptr("plan"), Steps: []StepEdit{{Title: ptr("one")}}})
			actor := identity.ActorID("root")
			r := AssignRequest{Assignee: "impl", Task: "task", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[0].ID}}}
			want := ErrInvalid
			switch name {
			case "empty actor":
				actor = ""
			case "empty assignee":
				r.Assignee = ""
			case "empty task":
				r.Task = ""
			case "unknown plan":
				r.Scope.PlanID = "missing"
				want = ErrNotFound
			case "wrong owner":
				actor = "other"
				want = ErrForbidden
			case "empty scope":
				r.Scope.StepIDs = nil
			case "repeated scope":
				r.Scope.StepIDs = append(r.Scope.StepIDs, r.Scope.StepIDs[0])
			case "unknown step":
				r.Scope.StepIDs = []StepID{"missing"}
				want = ErrNotFound
			}
			if _, err := s.AssignWork(actor, r); !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
}
func TestSubmissionProgressAndReadValidation(t *testing.T) {
	s, p, w := fixture(t)
	if _, err := s.GetWork("root", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetPlan("root", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetPlan("", p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.GetSubmission("root", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetAudit("root", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.UpdateProgress("impl", ProgressUpdate{WorkTarget: WorkTarget{ID: "missing", ExpectedRevision: 1}}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.UpdateProgress("impl", ProgressUpdate{WorkTarget: target(w), Steps: []StepProgress{{ID: p.Steps[0].ID}, {ID: p.Steps[0].ID}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.SubmitWork("impl", SubmitRequest{WorkTarget: target(w), Summary: "premature"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	var err error
	w, err = s.UpdateProgress("impl", ProgressUpdate{WorkTarget: target(w), Note: ptr("working"), Blocker: ptr("waiting"), Steps: []StepProgress{{ID: p.Steps[0].ID, Status: ptr(Blocked), Note: ptr("dependency")}}})
	if err != nil || w.Note != "working" {
		t.Fatal(w, err)
	}
	plan, _ := s.GetPlan("root", p.ID)
	if plan.Steps[0].Note != "dependency" {
		t.Fatal(plan)
	}
	if _, err := s.SubmitWork("impl", SubmitRequest{WorkTarget: target(w), Summary: "blocked"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	w, _ = s.UpdateProgress("impl", ProgressUpdate{WorkTarget: target(w), Blocker: ptr("")})
	w = ready(t, s, w)
	if _, err := s.SubmitWork("impl", SubmitRequest{WorkTarget: target(w), Summary: "ok", Artifacts: []ArtifactRef{{URI: " "}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	sub := submit(t, s, w)
	if _, err := s.GetSubmission("stranger", sub.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	current, _ := s.GetWork("root", w.ID)
	if _, err := s.SubmitWork("impl", SubmitRequest{WorkTarget: target(current), Summary: "again"}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	if _, err := s.Reassign("root", ReassignRequest{WorkTarget: target(current), Assignee: "new"}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	a := review(t, s, w.ID, sub.ID)
	got, err := s.GetSubmission("reviewer", sub.ID)
	if err != nil || len(got.Steps) != 2 {
		t.Fatal(got, err)
	}
	if _, err := s.UpdateProgress("reviewer", ProgressUpdate{WorkTarget: target(a), Steps: []StepProgress{{ID: p.Steps[0].ID}}}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
func TestAuditValidationAndAccess(t *testing.T) {
	s, p, w := fixture(t)
	w = ready(t, s, w)
	sub := submit(t, s, w)
	current, _ := s.GetWork("root", w.ID)
	for _, r := range []AssignAuditRequest{{WorkTarget: target(current), SubmissionID: sub.ID}, {WorkTarget: target(current), SubmissionID: sub.ID, Auditor: "impl"}, {WorkTarget: target(current), SubmissionID: "wrong", Auditor: "reviewer"}} {
		if _, err := s.AssignAudit("root", r); err == nil {
			t.Fatal("invalid audit accepted")
		}
	}
	if _, err := s.AssignAudit("other", AssignAuditRequest{WorkTarget: target(current)}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	a := review(t, s, w.ID, sub.ID)
	finding := Finding{StepIDs: []StepID{p.Steps[0].ID}, Description: "bad", RequiredChange: "fix", Verification: "test"}
	for _, name := range []string{"summary", "verdict", "pass findings", "fail no findings", "description", "scope missing", "scope outside"} {
		t.Run(name, func(t *testing.T) {
			r := AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Fail, Summary: "check", Findings: []Finding{finding}}
			want := ErrInvalid
			switch name {
			case "summary":
				r.Summary = ""
			case "verdict":
				r.Verdict = "unknown"
			case "pass findings":
				r.Verdict = Pass
			case "fail no findings":
				r.Findings = nil
			case "description":
				r.Findings[0].Description = ""
			case "scope missing":
				r.Findings[0].StepIDs = nil
			case "scope outside":
				r.Findings[0].StepIDs = []StepID{p.Steps[2].ID}
				want = ErrForbidden
			}
			if _, err := s.SubmitAudit("reviewer", r); !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
	audit, err := s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Fail, Summary: "check", Findings: []Finding{finding}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAudit("stranger", audit.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	for _, actor := range []identity.ActorID{"root", "impl", "reviewer"} {
		got, err := s.GetAudit(actor, audit.ID)
		if err != nil || got.ID != audit.ID {
			t.Fatal(got, err)
		}
	}
	repair, _ := assignRepairForTest(t, s, audit)
	repair, err = s.Reassign("root", ReassignRequest{WorkTarget: target(repair), Assignee: "repairer"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetAudit("repairer", audit.ID); err != nil || got.ID != audit.ID {
		t.Fatal(got, err)
	}
	if _, err := s.GetAudit("", audit.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Reassign("root", ReassignRequest{WorkTarget: target(repair)}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.Reassign("other", ReassignRequest{WorkTarget: target(repair)}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Cancel("other", CancelRequest{WorkTarget: target(repair), Reason: "stop"}); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Cancel("root", CancelRequest{WorkTarget: target(repair)}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	if _, err := s.SubmitAudit("impl", AuditRequest{WorkTarget: target(w)}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestCompletedAndReservedStepsCannotBeEditedOrCancelled(t *testing.T) {
	s, p, w := fixture(t)
	if _, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Cancel: []StepID{p.Steps[0].ID}}); !errors.Is(err, ErrReserved) {
		t.Fatal(err)
	}
	w = ready(t, s, w)
	sub := submit(t, s, w)
	a := review(t, s, w.ID, sub.ID)
	if _, err := s.SubmitAudit("reviewer", AuditRequest{WorkTarget: target(a), SubmissionID: sub.ID, Verdict: Pass, Summary: "verified"}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []PlanUpdate{{PlanID: &p.ID, ExpectedRevision: &p.Revision, Steps: []StepEdit{{ID: &p.Steps[0].ID, Title: ptr("change")}}}, {PlanID: &p.ID, ExpectedRevision: &p.Revision, Cancel: []StepID{p.Steps[0].ID}}} {
		if _, err := s.UpdatePlan("root", u); !errors.Is(err, ErrState) {
			t.Fatal(err)
		}
	}
	if _, err := s.AssignWork("root", AssignRequest{Assignee: "other", Task: "redo", Scope: w.Scope}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
}
