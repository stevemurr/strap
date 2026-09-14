package work

import "testing"

func TestReportChecksRecordedEvidenceAndPreservesInheritedBinding(t *testing.T) {
	var evidence ExecutionEvidence
	s := New(WithEvidenceLookup(func(ref string) (ExecutionEvidence, error) {
		if ref != "execution:known" {
			return ExecutionEvidence{}, ErrNotFound
		}
		return evidence, nil
	}))
	w, _ := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "inspect"})
	request := func(ref string) ReportWorkProgressRequest {
		return ReportWorkProgressRequest{WorkTarget: WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, AssignedAtRevision: w.AssignedAtRevision, Findings: []ProgressFindingDraft{{Claim: "observed", Basis: Observed, Evidence: []EvidenceRef{{URI: ref}}}}}
	}
	evidence = ExecutionEvidence{WorkID: "other", AssignedAtRevision: 1, Actor: "r"}
	for _, ref := range []string{"execution:forged", "execution:known"} {
		if _, err := s.ReportWorkProgress("r", request(ref)); err == nil {
			t.Fatal("accepted", ref)
		}
	}
	evidence.WorkID = w.ID
	w, _ = s.Reassign("root", ReassignRequest{WorkTarget: WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Assignee: "next"})
	r, err := s.ReportWorkProgress("next", request("execution:known"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.GetProgressFinding("root", r.FindingIDs[0])
	if err != nil || f.Evidence[0].URI != "execution:known" || evidence.Actor != "r" {
		t.Fatal(f, err)
	}
}
