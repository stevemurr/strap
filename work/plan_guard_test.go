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

// Edits that change nothing are rejected and consume no revision, so a model
// re-sending a step's current values gets an error instead of a fresh
// revision to re-send it with.
func TestNoOpPlanEditsAreRejected(t *testing.T) {
	s := New()
	p, err := s.UpdatePlan("root", PlanUpdate{Title: ptr("Plan"), Steps: []StepEdit{{Title: ptr("Implement"), AcceptanceCriteria: ptr([]string{"builds"})}}})
	if err != nil {
		t.Fatal(err)
	}
	rev := p.Revision
	same := StepEdit{ID: &p.Steps[0].ID, Title: ptr("Implement"), AcceptanceCriteria: ptr([]string{"builds"})}
	if _, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &rev, Steps: []StepEdit{same}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no-op step edit accepted: %v", err)
	}
	titleOnly := StepEdit{ID: &p.Steps[0].ID, Title: ptr(" Implement ")}
	if _, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &rev, Steps: []StepEdit{titleOnly}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("whitespace-only title change accepted: %v", err)
	}
	if _, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &rev, Title: ptr("Plan")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no-op rename accepted: %v", err)
	}
	if got, err := s.GetPlan("root", p.ID); err != nil || got.Revision != rev {
		t.Fatal("rejected edits must not consume a revision", got.Revision, rev, err)
	}
	if _, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &rev, Order: []StepID{p.Steps[0].ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no-op reorder accepted: %v", err)
	}
	criteria := StepEdit{ID: &p.Steps[0].ID, AcceptanceCriteria: ptr([]string{"builds", "vets"})}
	if p, err = s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &rev, Steps: []StepEdit{criteria}}); err != nil || p.Revision != rev+1 {
		t.Fatal("real change rejected", err)
	}
}
