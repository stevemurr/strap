package work

import (
	"errors"
	"testing"
)

func TestDuplicateResearchFindingsDoNotChangeWork(t *testing.T) {
	s := New()
	w, err := s.AssignResearch("root", ResearchAssignRequest{Assignee: "r", Task: "Compare design"})
	if err != nil {
		t.Fatal(err)
	}
	req := reportRequest(w)
	req.Findings = []ProgressFindingDraft{observed("source differs")}
	report := mustReport(t, s, w, req)
	w = current(t, s, w.ID)
	id := report.FindingIDs[0]
	_, err = s.SubmitResearch("r", SubmitResearchRequest{WorkTarget: target(w), Summary: "summary", FindingIDs: []ProgressFindingID{id, id}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	got := current(t, s, w.ID)
	if got.Revision != w.Revision || got.State != w.State {
		t.Fatal("rejected brief changed work")
	}
}
