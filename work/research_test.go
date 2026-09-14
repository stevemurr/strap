package work

import (
	"errors"
	"testing"
)

func TestResearchCancellationHasNoImplementationEffects(t *testing.T) {
	s := New()
	w, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "researcher", Task: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitWork("researcher", SubmitRequest{WorkTarget: target(w), Summary: "not implementation"}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	w, err = s.Reassign("root", ReassignRequest{WorkTarget: target(w), Assignee: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReportWorkProgress("researcher", reportRequest(w)); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	w, err = s.Cancel("root", CancelRequest{WorkTarget: target(w), Reason: "question withdrawn"})
	if err != nil || w.State != Cancelled || w.ParentID != "" || w.Scope != nil {
		t.Fatal(w, err)
	}
	if len(s.works) != 1 || len(s.submissions) != 0 || len(s.audits) != 0 || len(s.plans) != 0 {
		t.Fatal("research changed unrelated ledgers")
	}
	for _, e := range s.PendingEvents(0) {
		if e.Kind == ReviewRequested || e.Work.ID == "" {
			t.Fatal(e)
		}
	}
	if _, err = s.Reassign("root", ReassignRequest{WorkTarget: target(w), Assignee: "other"}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
}

func TestResearchAssignmentRejectsImplementationSelectors(t *testing.T) {
	for _, r := range []AssignmentRequest{
		{Kind: Research, Assignee: "r", Task: "inspect", Scope: &Scope{PlanID: "p"}},
		{Kind: Research, Assignee: "r", Task: "inspect", SubmissionID: "s"},
		{Kind: Research, Assignee: "r", Task: "inspect", ExpectedRevision: 1},
	} {
		if !errors.Is(r.Validate(), ErrInvalid) {
			t.Fatal(r)
		}
	}
}
