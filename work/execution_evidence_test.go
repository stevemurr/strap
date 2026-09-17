package work

import (
	"errors"
	"strings"
	"testing"
)

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
		return ReportWorkProgressRequest{WorkID: w.ID, Findings: []ProgressFindingDraft{{Claim: "observed", Basis: Observed, Evidence: []EvidenceRef{{URI: ref}}}}}
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

// A model with no evidence_ref to copy composes one that reads like a summary
// of what it did. The rejection has to name that URI and where real ones come
// from, or the only thing it tells the author is that something, somewhere,
// was not found.
func TestFabricatedEvidenceRejectionNamesTheURI(t *testing.T) {
	s := New(WithEvidenceLookup(func(string) (ExecutionEvidence, error) {
		return ExecutionEvidence{}, ErrNotFound
	}))
	w, _ := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "inspect"})
	const forged = "execution:evidence:build-success"
	_, err := s.ReportWorkProgress("r", ReportWorkProgressRequest{
		WorkID:   w.ID,
		Findings: []ProgressFindingDraft{{Claim: "build passes", Basis: Observed, Evidence: []EvidenceRef{{URI: forged}}}},
	})
	if err == nil {
		t.Fatal("accepted a composed execution URI")
	}
	for _, want := range []string{forged, "evidence_ref", "never one composed by hand"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection omits %q: %v", want, err)
		}
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("lost the not-found classification: %v", err)
	}
}

// The same report carries three other host-issued references, and each one used
// to fail with the same unattributable sentinel.
func TestUnknownReferencesNameTheirSubject(t *testing.T) {
	s := New()
	w, _ := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "inspect"})
	target := WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}
	_, err := s.ReportWorkProgress("r", ReportWorkProgressRequest{WorkID: target.ID,
		Position: &WorkPosition{Objective: "o", Dependencies: []ProgressDependency{{Need: "waiting", WorkID: "work-absent"}}}})
	if err == nil || !strings.Contains(err.Error(), "work-absent") {
		t.Fatalf("dependency rejection: %v", err)
	}
	_, err = s.ReportWorkProgress("r", ReportWorkProgressRequest{WorkID: target.ID,
		Findings: []ProgressFindingDraft{{Claim: "retracted", Basis: Retracted, Supersedes: "finding-absent"}}})
	if err == nil || !strings.Contains(err.Error(), "finding-absent") {
		t.Fatalf("supersedes rejection: %v", err)
	}
}
