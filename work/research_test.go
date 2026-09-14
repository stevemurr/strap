package work

import (
	"context"
	"errors"
	"strings"
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

func TestResearchBriefDeliveryAndPassiveReconstruction(t *testing.T) {
	view := NewReadModel()
	s := New(WithReporter(ReporterFunc(func(_ context.Context, e Event) error { view.Apply(*e.Change); return nil })))
	w, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "Compare design"})
	if err != nil {
		t.Fatal(err)
	}
	req := reportRequest(w)
	req.Position = &WorkPosition{Objective: "check", Blocker: "not runnable"}
	req.Findings = []ProgressFindingDraft{observed("source differs")}
	report := mustReport(t, s, w, req)
	w = current(t, s, w.ID)
	r := SubmitResearchRequest{WorkTarget: target(w), AssignedAtRevision: w.AssignedAtRevision, Summary: "Inconclusive at runtime", FindingIDs: report.FindingIDs, OpenQuestions: []string{"Does it run?"}, ProposedSteps: []ProposedStep{{Title: "Verify behavior", AcceptanceCriteria: []string{"run check"}}}}
	receipt, err := s.SubmitResearch("r", r)
	if err != nil {
		t.Fatal(err)
	}
	r.ProposedSteps[0].AcceptanceCriteria[0] = "changed"
	brief, err := view.GetResearchBrief("root", receipt.BriefID)
	if err != nil || brief.ProposedSteps[0].AcceptanceCriteria[0] != "run check" {
		t.Fatal(brief, err)
	}
	progress, err := view.GetWorkProgress("r", w.ID)
	if err != nil || progress.Current.State != Delivered || progress.Current.ActiveBlocker != "" || progress.LastReportedPosition.Value.Blocker != "not runnable" {
		t.Fatal(progress, err)
	}
	w = current(t, s, w.ID)
	r.WorkTarget = target(w)
	if _, err = s.SubmitResearch("r", r); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	if _, err = s.Cancel("root", CancelRequest{WorkTarget: target(w), Reason: "late"}); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	if len(s.plans) != 0 || len(s.audits) != 0 || len(s.submissions) != 0 {
		t.Fatal("brief accepted implementation")
	}
	if _, err = view.GetResearchBrief("other", receipt.BriefID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}

func TestResearchBriefRejectsSupersededAndOversizedContent(t *testing.T) {
	s := New()
	w, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	req := reportRequest(w)
	req.Findings = []ProgressFindingDraft{observed("old")}
	first := mustReport(t, s, w, req)
	w = current(t, s, w.ID)
	req = reportRequest(w)
	f := observed("new")
	f.Supersedes = first.FindingIDs[0]
	req.Findings = []ProgressFindingDraft{f}
	mustReport(t, s, w, req)
	w = current(t, s, w.ID)
	r := SubmitResearchRequest{WorkTarget: target(w), AssignedAtRevision: w.AssignedAtRevision, Summary: "summary", FindingIDs: first.FindingIDs}
	if _, err = s.SubmitResearch("r", r); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	r.FindingIDs = nil
	r.OpenQuestions = make([]string, 32)
	for i := range r.OpenQuestions {
		r.OpenQuestions[i] = strings.Repeat("x", 4096)
	}
	if _, err = s.SubmitResearch("r", r); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if current(t, s, w.ID).State != Active || len(s.researchBriefs) != 0 {
		t.Fatal("invalid brief mutated state")
	}
}
