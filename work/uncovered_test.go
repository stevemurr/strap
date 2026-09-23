package work

import (
	"slices"
	"testing"
)

func TestUncoveredStepsListsOpenStepsNoLiveWorkScopes(t *testing.T) {
	v := NewReadModel()
	plan := Plan{ID: "plan-a", Owner: "root", Steps: []Step{
		{ID: "impl", Title: "Implement", Status: Completed},
		{ID: "verify", Title: "Verify go build and go vet pass", Status: Pending},
		{ID: "next", Title: "Next phase", Status: Pending},
		{ID: "dropped", Title: "Dropped", Status: CancelledStep},
		{ID: "stuck", Title: "Stuck", Status: InProgress},
	}}
	other := Plan{ID: "plan-b", Owner: "someone-else", Steps: []Step{{ID: "theirs", Status: Pending}}}
	v.Apply(Change{Plans: []Plan{plan, other}, Works: []Work{
		{ID: "w1", Kind: Implementation, State: Accepted, Owner: "root", Scope: &Scope{PlanID: "plan-a", StepIDs: []StepID{"impl"}}},
		{ID: "w2", Kind: Implementation, State: Active, Owner: "root", Scope: &Scope{PlanID: "plan-a", StepIDs: []StepID{"next"}}},
		// Terminal work no longer covers its steps: "stuck" is open again.
		{ID: "w3", Kind: Implementation, State: Cancelled, Owner: "root", Scope: &Scope{PlanID: "plan-a", StepIDs: []StepID{"stuck"}}},
	}})
	var got []StepID
	for _, o := range v.UncoveredSteps("root") {
		if o.PlanID != "plan-a" {
			t.Fatalf("listed another owner's plan: %+v", o)
		}
		got = append(got, o.Step.ID)
	}
	if want := []StepID{"verify", "stuck"}; !slices.Equal(got, want) {
		t.Fatalf("uncovered %v, want %v", got, want)
	}
}
