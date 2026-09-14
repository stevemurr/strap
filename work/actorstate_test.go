package work

import (
	"strings"
	"testing"
)

// The wake-time state block names every id and revision an actor needs, from
// the store, scoped to what that actor owns or is assigned.
func TestActorStateNamesOwnedAndAssignedRecords(t *testing.T) {
	s, p, w := fixture(t)
	root := s.ActorState("root")
	if len(root.Plans) != 1 || len(root.Owned) != 1 || len(root.Assigned) != 0 {
		t.Fatalf("root state: %+v", root)
	}
	plan := root.Plans[0]
	if plan.PlanID != p.ID || plan.Revision != p.Revision || plan.Title != "storage" || len(plan.Steps) != 3 {
		t.Fatalf("plan state: %+v", plan)
	}
	if plan.Steps[0].ReservedBy != w.ID || plan.Steps[1].ReservedBy != w.ID || plan.Steps[2].ReservedBy != "" || plan.Steps[0].Status != Pending {
		t.Fatalf("reservations: %+v", plan.Steps)
	}
	owned := root.Owned[0]
	if owned.WorkID != w.ID || owned.Assignee != "impl" || owned.Revision != 1 || owned.AssignedAtRevision != 0 || owned.Steps != nil {
		t.Fatalf("owned state: %+v", owned)
	}

	worker := s.ActorState("impl")
	if len(worker.Plans) != 0 || len(worker.Owned) != 0 || len(worker.Assigned) != 1 {
		t.Fatalf("worker state: %+v", worker)
	}
	assigned := worker.Assigned[0]
	if assigned.WorkID != w.ID || assigned.AssignedAtRevision != 1 || assigned.Assignee != "" || len(assigned.Steps) != 2 || assigned.Steps[0].StepID != p.Steps[0].ID {
		t.Fatalf("assigned state: %+v", assigned)
	}
	if got := s.ActorState("stranger"); len(got.Plans)+len(got.Owned)+len(got.Assigned) != 0 {
		t.Fatalf("stranger sees state: %+v", got)
	}
	if got := s.ActorState(""); len(got.Plans)+len(got.Owned)+len(got.Assigned) != 0 {
		t.Fatalf("anonymous sees state: %+v", got)
	}

	// Progress and cancellation flow through: statuses update, cancelled steps
	// and terminal work disappear, long titles are shortened.
	w = ready(t, s, w)
	if got := s.ActorState("impl").Assigned[0]; got.Revision != w.Revision || got.Steps[0].Status != ReadyForReview {
		t.Fatalf("progress not reflected: %+v", got)
	}
	cancelled, err := s.UpdatePlan("root", PlanUpdate{PlanID: &p.ID, ExpectedRevision: &p.Revision, Cancel: []StepID{p.Steps[2].ID}, Steps: []StepEdit{{Title: ptr(strings.Repeat("long ", 40))}}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.ActorState("root").Plans[0]
	if got.Revision != cancelled.Revision || len(got.Steps) != 3 || !strings.HasSuffix(got.Steps[2].Title, "…") || len(got.Steps[2].Title) > stateTitleLimit+len("…") {
		t.Fatalf("plan after cancel/add: %+v", got)
	}
	if _, err := s.Cancel("root", CancelRequest{WorkTarget: target(w), Reason: "done"}); err != nil {
		t.Fatal(err)
	}
	if got := s.ActorState("root"); len(got.Owned) != 0 || got.Plans[0].Steps[0].ReservedBy != "" {
		t.Fatalf("cancelled work still listed: %+v", got)
	}
	if got := s.ActorState("impl"); len(got.Assigned) != 0 {
		t.Fatalf("cancelled assignment still listed: %+v", got)
	}
}
