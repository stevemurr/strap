package work

import (
	"errors"
	"testing"
)

// Reviews, research and experiments complete the plan steps they are scoped to when
// delivered, as audit acceptance completes implementation's; cancelling them
// releases the steps.
func TestDeliveryCompletesScopedSteps(t *testing.T) {
	s, issued, _ := experimentStore(t)
	p, err := s.UpdatePlan("manager", PlanUpdate{Title: ptr("plan"), Steps: []StepEdit{{Title: ptr("Survey the workspace")}, {Title: ptr("Measure rendering")}, {Title: ptr("Implement")}}})
	if err != nil {
		t.Fatal(err)
	}
	survey, measure := &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[0].ID}}, &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[1].ID}}
	status := func(i int) StepStatus { return s.plans[p.ID].Steps[i].Status }

	review, err := s.AssignInvestigation("manager", InvestigationRequest{Kind: Review, Assignee: "reviewer", Task: "Survey", Scope: survey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignInvestigation("manager", InvestigationRequest{Kind: Review, Assignee: "reviewer", Task: "Again", Scope: survey}); !errors.Is(err, ErrReserved) {
		t.Fatal("a step was scoped to two live items:", err)
	}
	if _, err = s.SubmitBrief("reviewer", SubmitBriefRequest{WorkTarget: WorkTarget{ID: review.ID, ExpectedRevision: review.Revision}, Summary: "Three packages"}); err != nil {
		t.Fatal(err)
	}
	if status(0) != Completed || s.reserved[p.Steps[0].ID] != "" {
		t.Fatalf("delivered research left its step %s", status(0))
	}

	exp, err := s.AssignExperiment("manager", ExperimentAssignRequest{Assignee: "experimenter", Task: "Measure", Scope: measure})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel("manager", CancelRequest{WorkTarget: WorkTarget{ID: exp.ID, ExpectedRevision: exp.Revision}, Reason: "rescoped"}); err != nil {
		t.Fatal(err)
	}
	if status(1) != Pending || s.reserved[p.Steps[1].ID] != "" {
		t.Fatalf("a cancelled experiment kept its step %s", status(1))
	}
	exp, err = s.AssignExperiment("manager", ExperimentAssignRequest{Assignee: "experimenter", Task: "Measure", Scope: measure})
	if err != nil {
		t.Fatal("a released step could not be assigned again:", err)
	}
	h, err := s.RecordHypothesis("experimenter", RecordHypothesisRequest{WorkID: exp.ID, Statement: "s", Prediction: "p", Method: "m"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RecordResult("experimenter", RecordResultRequest{WorkID: exp.ID, HypothesisID: h.Hypothesis.ID, Verdict: Refuted, Observed: "fast", EvidenceRefs: []string{issued.run(s, exp, "experimenter")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitExperiment("experimenter", SubmitExperimentRequest{WorkTarget: WorkTarget{ID: exp.ID, ExpectedRevision: r.WorkRevision}, Summary: "fast", Method: ExperimentMethod{ReproduceCommand: "go test -bench ."}}); err != nil {
		t.Fatal(err)
	}
	if status(1) != Completed || status(2) != Pending {
		t.Fatalf("steps after delivery: %s %s", status(1), status(2))
	}
	// Researchers report no step statuses; delivery is the transition.
	other, err := s.AssignInvestigation("manager", InvestigationRequest{Kind: Review, Assignee: "reviewer", Task: "Look", Scope: &Scope{PlanID: p.ID, StepIDs: []StepID{p.Steps[2].ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReportWorkProgress("reviewer", ReportWorkProgressRequest{WorkID: other.ID, Steps: []StepProgress{{ID: p.Steps[2].ID, Status: ptr(InProgress)}}}); !errors.Is(err, ErrForbidden) {
		t.Fatal("research reported a step status:", err)
	}
}
