package work

import (
	"errors"
	"strings"
	"testing"
)

// Adding a step whose title matches a live step is the signature of a plan
// snapshot copied back as new steps; the rejection names the existing step.
func TestAddedStepWithLiveTitleNamesTheExistingStep(t *testing.T) {
	s, p, _ := fixture(t)
	_, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Steps: []StepEdit{{Title: ptr(" write ")}}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `step "write" already exists as `+string(p.Steps[0].ID)) || !strings.Contains(err.Error(), "edit_step") {
		t.Fatalf("duplicate accepted or unexplained: %v", err)
	}
	// Cancelled steps free their title, and creation never applies the guard.
	cancelled, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Cancel: []StepID{p.Steps[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &cancelled.Revision, Steps: []StepEdit{{Title: ptr("integrate")}}}); err != nil {
		t.Fatalf("cancelled title still reserved: %v", err)
	}
	if _, err := s.UpdatePlan("root", PlanUpdate{Title: ptr("twins"), Steps: []StepEdit{{Title: ptr("same")}, {Title: ptr("same")}}}); err != nil {
		t.Fatalf("creation guarded: %v", err)
	}
}
