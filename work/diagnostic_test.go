package work

import "testing"

func TestDiagnosticSelectionSurvivesProgressButNotReassignment(t *testing.T) {
	s := New()
	a, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "B"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.ReportWorkProgress("r", ReportWorkProgressRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, Position: &WorkPosition{Objective: "inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := s.AdmitResearchDiagnostic("r", a.ID)
	if err != nil || captured.ID != a.ID || captured.ID == b.ID {
		t.Fatal(captured, err)
	}
	a, err = s.Reassign("root", ReassignRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: report.WorkRevision}, Assignee: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitResearchDiagnostic("r", a.ID); err != ErrForbidden {
		t.Fatal(err)
	}
	// Reassigned back: the researcher is the current assignee again, so a new
	// diagnostic is admitted and stamped with the current binding. The earlier
	// capture keeps the binding it was taken under.
	a, err = s.Reassign("root", ReassignRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, Assignee: "r"})
	if err != nil {
		t.Fatal(err)
	}
	readmitted, err := s.AdmitResearchDiagnostic("r", a.ID)
	if err != nil || readmitted.AssignedAtRevision != a.AssignedAtRevision {
		t.Fatal(readmitted, err)
	}
	if captured.AssignedAtRevision == a.AssignedAtRevision || captured.Assignee != "r" {
		t.Fatal("relabelled prior execution")
	}
	if _, err = s.AdmitResearchDiagnostic("root", a.ID); err != ErrForbidden {
		t.Fatal(err)
	}
}
