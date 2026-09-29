package harness_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// An assignment naming a role as its assignee creates an agent of that role
// for the work (assign_task names the kind's role for a null assignee); one
// that fails leaves no idle agent behind, and a role that cannot take the
// work creates nothing.
func TestAssignmentToRoleCreatesItsAgent(t *testing.T) {
	ctx := context.Background()
	s, err := harness.New(ctx, testConfig(t, false), harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(ctx)
	before := len(s.Agents())
	w, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: "implementor", Task: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.InspectAgent(w.Assignee, conversation.InspectOptions{})
	if err != nil || in.Role != roster.Implementor || w.Assignee == "implementor" || len(s.Agents()) != before+1 {
		t.Fatal(w.Assignee, in, err)
	}

	// Unscoped research needs no plan; a plan that does not exist fails the
	// assignment after the reviewer was created, which must then stop.
	_, err = s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Review, Assignee: "reviewer", Task: "look", Scope: &work.Scope{PlanID: "plan-absent", StepIDs: []work.StepID{"step-absent"}}})
	if err == nil {
		t.Fatal("assignment to a missing plan succeeded")
	}
	for _, a := range s.Agents() {
		in, _ := s.InspectAgent(a.ID, conversation.InspectOptions{})
		if in.Role == roster.Reviewer && !in.State.Terminal() && in.State != agent.StopRequested {
			t.Fatalf("failed assignment left reviewer %s %s", a.ID, in.State)
		}
	}

	if _, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: "reviewer", Task: "build it"}); err == nil {
		t.Fatal("a reviewer took implementation")
	}
	// The kind names the role: naming another role, or an agent of one, is
	// refused with the role that does the kind.
	if _, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Review, Assignee: "web_researcher", Task: "read the folder"}); err == nil || !strings.Contains(err.Error(), "review work goes to a reviewer") {
		t.Fatal(err)
	}
	researcher := createWorker(t, s, roster.WebResearcher)
	if _, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Review, Assignee: researcher, Task: "read the folder"}); err == nil || !strings.Contains(err.Error(), "which goes to a reviewer") {
		t.Fatal(err)
	}
}
