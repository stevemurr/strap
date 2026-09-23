package work

import (
	"errors"
	"strings"
	"testing"
)

// Rejections name the instance, not only the class: the id, the revision
// numbers and the corrective step. errors.Is contracts are unchanged.
func TestRejectionsCarryIdsRevisionsAndNextStep(t *testing.T) {
	s, p, w := fixture(t)
	expect := func(err error, sentinel error, fragments ...string) {
		t.Helper()
		if !errors.Is(err, sentinel) {
			t.Fatalf("%v is not %v", err, sentinel)
		}
		for _, fragment := range fragments {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("%q lacks %q", err, fragment)
			}
		}
	}
	_, err := s.SubmitWork(w.Assignee, SubmitRequest{WorkTarget: WorkTarget{ID: w.ID, ExpectedRevision: 9}, Summary: "x"})
	expect(err, ErrConflict, string(w.ID)+" is at revision 1", "expected_revision was 9", "work_revision from your last receipt")

	_, err = s.SubmitWork(w.Assignee, SubmitRequest{WorkTarget: target(w), Summary: "premature"})
	expect(err, ErrInvalid, "before submit_work: "+string(p.Steps[0].ID)+" is pending, "+string(p.Steps[1].ID)+" is pending", "report_work_progress steps")

	_, err = s.ReportWorkProgress(w.Assignee, ReportWorkProgressRequest{WorkID: "work-absent", Position: &WorkPosition{Objective: "o"}})
	expect(err, ErrNotFound, "work work-absent", "live work visible to you")

	_, err = s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: ptr(p.Revision + 5), Steps: []StepEdit{{ID: &p.Steps[2].ID, Title: ptr("t")}}})
	expect(err, ErrConflict, "plan "+string(p.ID)+" is at revision", "revision from get_plan")

	_, err = s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Steps: []StepEdit{{ID: &p.Steps[0].ID, Title: ptr("t")}}})
	expect(err, ErrReserved, "step "+string(p.Steps[0].ID)+" is reserved by "+string(w.ID), "omit reserved steps")

	missing := StepID("step-404")
	_, err = s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Steps: []StepEdit{{ID: &missing, Title: ptr("t")}}})
	expect(err, ErrNotFound, "step step-404 is not in plan "+string(p.ID))

	_, err = s.AssignWork("root", AssignRequest{Assignee: "other", Task: "dup", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[0].ID}}})
	expect(err, ErrReserved, "step "+string(p.Steps[0].ID)+" is reserved by "+string(w.ID), "scope only available steps")

	_, err = s.GetResearchBrief("root", "report-5")
	expect(err, ErrNotFound, "report-5 is a progress report; read it with get_work_progress mode report", "no brief has been delivered to you")

	// A real ID in the wrong slot names the reader for its kind.
	_, err = s.GetWorkProgress("root", "brief-3")
	expect(err, ErrNotFound, "brief-3 is a delivered research brief; read it with get_research_brief")
	_, err = s.GetWorkProgress("root", "work-absent")
	expect(err, ErrNotFound, "work work-absent", "live work visible to you")
	_, err = s.ProgressReports("root", "finding-2")
	expect(err, ErrNotFound, "finding-2 is a ledger finding; read it with get_work_progress mode finding")
	_, err = s.GetWorkProgressReport("root", "brief-3")
	expect(err, ErrNotFound, "read it with get_research_brief")
	_, err = s.GetWorkProgressReport("root", "report-404")
	expect(err, ErrNotFound, "progress report report-404", "report_work_progress receipts")
	_, err = s.GetProgressFinding("root", "run-7")
	expect(err, ErrNotFound, "run-7 is a researcher's deep research run", "get_research_run")
	_, err = s.GetProgressFinding("root", "finding-404")
	expect(err, ErrNotFound, "finding finding-404", "get_work_progress mode findings")
	_, err = s.GetResearchBrief("root", "brief-9")
	expect(err, ErrNotFound, "research brief brief-9")

	research, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "researcher", Task: "look"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitResearch("researcher", SubmitResearchRequest{WorkTarget: target(research), Summary: "s", FindingIDs: []ProgressFindingID{"finding-1"}})
	expect(err, ErrNotFound, "finding finding-1 was never recorded for "+string(research.ID), "omit finding_ids if none were recorded")

	// Not-found rejections name what does exist for the caller, so a guessed
	// id turns into a lookup instead of another guess.
	_, err = s.GetResearchBrief("root", "brief-guess")
	expect(err, ErrNotFound, "no brief has been delivered to you", "research not yet delivered: "+string(research.ID))
	delivered, err := s.SubmitResearch("researcher", SubmitResearchRequest{WorkTarget: target(research), Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GetResearchBrief("root", "brief-guess")
	expect(err, ErrNotFound, "delivered briefs: "+string(delivered.BriefID)+" ("+string(research.ID)+")")
	if _, err = s.GetResearchBrief("stranger", "brief-guess"); !strings.Contains(err.Error(), "no brief has been delivered to you") || strings.Contains(err.Error(), string(delivered.BriefID)) {
		t.Fatalf("listing leaked to a stranger: %v", err)
	}
	_, err = s.GetWork("impl", "work-guess")
	expect(err, ErrNotFound, "work work-guess", "live work visible to you: "+string(w.ID)+" (implementation, active, revision 1)")
	_, err = s.SubmitWork("impl", SubmitRequest{WorkTarget: WorkTarget{ID: "work-guess", ExpectedRevision: 1}, Summary: "x"})
	expect(err, ErrNotFound, "live work visible to you: "+string(w.ID))
	_, err = s.GetPlan("root", "plan-guess")
	expect(err, ErrNotFound, "plan plan-guess", "plans you own: "+string(p.ID)+" (revision 1)")
	_, err = s.UpdatePlan("root", PlanUpdate{PlanID: ptr(PlanID("plan-guess")), ExpectedRevision: ptr(Revision(1)), Steps: []StepEdit{{Title: ptr("t")}}})
	expect(err, ErrNotFound, "plans you own: "+string(p.ID))
	_, err = s.GetPlan("stranger", "plan-guess")
	expect(err, ErrNotFound, "you own no plan")
}
