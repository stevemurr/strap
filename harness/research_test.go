package harness_test

import (
	"context"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
	"testing"
)

func TestResearcherCreationAndConfiguration(t *testing.T) {
	ctx := context.Background()
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web = t.TempDir(), nil
	cfg.Researcher.Prompt.Role = "Independent research configuration"
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: idle{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(ctx)
	id := createWorker(t, s, roster.Researcher)
	got, err := s.InspectAgent(id, conversation.InspectOptions{})
	if err != nil || got.Role != roster.Researcher || got.State != agent.Idle {
		t.Fatal(got, err)
	}
	effective := s.Configuration().Researcher
	if effective.Prompt.Role != cfg.Researcher.Prompt.Role || !effective.InjectedProvider {
		t.Fatal(effective)
	}
	for _, tool := range effective.Tools {
		switch tool.Name {
		case "shell", "write_file", "edit_file", "assign_work", "submit_work", "submit_audit":
			t.Fatalf("researcher received %s", tool.Name)
		}
	}
}

func TestResearchAssignmentAndListing(t *testing.T) {
	ctx := context.Background()
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LocalTools = t.TempDir(), nil, false
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: idle{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(ctx)
	id := createWorker(t, s, roster.Researcher)
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: id, Task: "Inspect requirements"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListWork(ctx, s.Root(), work.ListQuery{Kind: work.Research})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != w.ID {
		t.Fatal(page, err)
	}
	if _, err = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: id, Task: "Change code"}); err == nil {
		t.Fatal("researcher received implementation")
	}
}
