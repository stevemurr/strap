package harness

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

type quietProvider struct{}

func (quietProvider) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: "ok"}, nil
}

func guardSession(t *testing.T, dir string) *Session {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LocalTools, cfg.LSP = dir, nil, dir != "", nil
	if dir == "" {
		cfg.Dir = t.TempDir()
	}
	cfg.DeepResearch.Enabled = false
	s, err := New(context.Background(), cfg, Dependencies{Provider: quietProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

// The shapes from the ladder traces: an implementation step that was assigned,
// a separate "verify" step that never was, and work still live at reply time.
func TestRootReplyCheckFlagsLiveWorkAndStepsNoWorkCovers(t *testing.T) {
	s := guardSession(t, "")
	ctx := context.Background()
	root := s.Root()
	title, impl, verify := "Implement Longest", "Implement Longest in uptime.go", "Verify go build and go vet pass"
	plan, err := s.UpdatePlan(ctx, root, work.PlanUpdate{Title: &title, Steps: []work.StepEdit{{Title: &impl}, {Title: &verify}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.rootReplyCheck(ctx, root); got != "" {
		t.Fatalf("no work yet (as in a research-only task) must not be held back: %q", got)
	}
	worker, err := s.CreateAgent(ctx, root, roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AssignWork(ctx, root, work.AssignmentRequest{Kind: work.Implementation, Assignee: worker.AgentID, Task: "implement",
		Scope: &work.Scope{PlanID: plan.ID, StepIDs: []work.StepID{plan.Steps[0].ID}}})
	if err != nil {
		t.Fatal(err)
	}
	notice := s.rootReplyCheck(ctx, root)
	for _, want := range []string{string(w.ID) + " (implementation, active): " + string(worker.AgentID) + " is working on it; wait with wait_for_input", string(plan.Steps[1].ID), verify, "cancel_steps"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice lacks %q: %q", want, notice)
		}
	}
	if strings.Contains(notice, string(plan.Steps[0].ID)) {
		t.Fatalf("the assigned step is covered: %q", notice)
	}
	plan, err = s.GetPlan(ctx, root, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePlan(ctx, root, work.PlanUpdate{PlanID: &plan.ID, ExpectedRevision: &plan.Revision, Cancel: []work.StepID{plan.Steps[1].ID}}); err != nil {
		t.Fatal(err)
	}
	if notice := s.rootReplyCheck(ctx, root); !strings.Contains(notice, string(w.ID)) || strings.Contains(notice, verify) {
		t.Fatalf("after cancelling the step only the live work remains: %q", notice)
	}
	if w, err = s.GetWork(ctx, root, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelWork(ctx, root, work.CancelRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Reason: "done elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if got := s.rootReplyCheck(ctx, root); !strings.Contains(got, string(plan.Steps[0].ID)) {
		t.Fatalf("cancelled work no longer covers its step: %q", got)
	}
}

// Waiting only helps while someone is working. Submitted work waits on the
// root's audit and a stopped assignee never reports: advising a wait there
// would stall the root.
func TestRootReplyCheckNamesTheNextActionNotAWait(t *testing.T) {
	s := guardSession(t, "")
	ctx := context.Background()
	root := s.Root()
	worker, err := s.CreateAgent(ctx, root, roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := s.AssignWork(ctx, root, work.AssignmentRequest{Kind: work.Implementation, Assignee: worker.AgentID, Task: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitWork(ctx, worker.AgentID, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: submitted.ID, ExpectedRevision: submitted.Revision}, Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateAgent(ctx, root, roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		t.Fatal(err)
	}
	orphaned, err := s.AssignWork(ctx, root, work.AssignmentRequest{Kind: work.Implementation, Assignee: other.AgentID, Task: "implement more"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopAgent(other.AgentID); err != nil {
		t.Fatal(err)
	}
	notice := s.rootReplyCheck(ctx, root)
	for _, want := range []string{
		string(submitted.ID) + " (implementation, needs_check): it is submitted and needs an independent audit; assign one with assign_audit",
		string(orphaned.ID) + " (implementation, active): its assignee " + string(other.AgentID) + " has stopped; reassign it with reassign_work",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice lacks %q: %q", want, notice)
		}
	}
	if strings.Contains(notice, "wait_for_input") {
		t.Fatalf("nothing here will arrive by waiting: %q", notice)
	}
}

func TestWorkspaceListingIsShallowFirstAndShownOnFirstWakeOnly(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"README.md", "stock.go", "go.mod", "cmd/tool/main.go", "cmd/tool/deep/x/y.go", ".git/HEAD", "node_modules/m/i.js", ".hidden"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws := workspaceListing(dir)
	want := []string{"README.md", "cmd/", "go.mod", "stock.go", "cmd/tool/", "cmd/tool/deep/", "cmd/tool/main.go"}
	if ws == nil || ws.Dir != dir || !slices.Equal(ws.Entries, want) || ws.More != 0 {
		t.Fatalf("listing %+v, want %v", ws, want)
	}
	s := guardSession(t, dir)
	wake := s.wakeContext(s.Root())
	m, err := wake(context.Background(), nil)
	if err != nil || m == nil || m.Workspace == nil || !slices.Contains(m.Workspace.Entries, "stock.go") || m.State != nil {
		t.Fatalf("first wake: %+v, %v", m, err)
	}
	if m, err := wake(context.Background(), nil); err != nil || m != nil {
		t.Fatalf("later wakes carry no listing: %+v, %v", m, err)
	}
}
