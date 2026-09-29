package work

import "testing"

func TestResearchRunSelectionSurvivesProgressButNotCancellation(t *testing.T) {
	s := New()
	a, err := s.AssignInvestigation("root", InvestigationRequest{Kind: WebResearch, Assignee: "r", Task: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AssignInvestigation("root", InvestigationRequest{Kind: WebResearch, Assignee: "r", Task: "B"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.ReportWorkProgress("r", ReportWorkProgressRequest{WorkID: a.ID, Position: &WorkPosition{Objective: "inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := s.AdmitResearchRun("r", a.ID)
	if err != nil || captured.ID != a.ID || captured.ID == b.ID || captured.Assignee != "r" {
		t.Fatal(captured, err)
	}
	if _, err = s.AdmitResearchRun("root", a.ID); err != ErrForbidden {
		t.Fatal(err)
	}
	if _, err = s.Cancel("root", CancelRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: report.WorkRevision}, Reason: "withdrawn"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitResearchRun("r", a.ID); err == nil {
		t.Fatal("cancelled research admitted a run")
	}
	if again, err := s.AdmitResearchRun("r", b.ID); err != nil || again.ID != b.ID {
		t.Fatal(again, err)
	}
}
