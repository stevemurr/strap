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
	report, err := s.ReportWorkProgress("r", ReportWorkProgressRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, AssignedAtRevision: a.AssignedAtRevision, Position: &WorkPosition{Objective: "inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := s.AdmitResearchDiagnostic("r", a.ID, a.AssignedAtRevision)
	if err != nil || captured.ID != a.ID || captured.ID == b.ID {
		t.Fatal(captured, err)
	}
	a, err = s.Reassign("root", ReassignRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: report.WorkRevision}, Assignee: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitResearchDiagnostic("r", a.ID, captured.AssignedAtRevision); err != ErrForbidden {
		t.Fatal(err)
	}
	a, err = s.Reassign("root", ReassignRequest{WorkTarget: WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, Assignee: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitResearchDiagnostic("r", a.ID, captured.AssignedAtRevision); err != ErrConflict {
		t.Fatal(err)
	}
	if captured.AssignedAtRevision == a.AssignedAtRevision || captured.Assignee != "r" {
		t.Fatal("relabelled prior execution")
	}
	if _, err = s.AdmitResearchDiagnostic("r", a.ID, 0); err != ErrConflict {
		t.Fatal(err)
	}
	if _, err = s.AdmitResearchDiagnostic("root", a.ID, a.AssignedAtRevision); err != ErrForbidden {
		t.Fatal(err)
	}
}
