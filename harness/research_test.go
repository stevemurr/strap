package harness_test

import (
	"context"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
	"strings"
	"testing"
)

func TestResearcherCreationAndConfiguration(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, true)
	cfg.WebResearcher.Prompt.Role = "Independent research configuration"
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(ctx)
	id := createWorker(t, s, roster.WebResearcher)
	got, err := s.InspectAgent(id, conversation.InspectOptions{})
	if err != nil || got.Role != roster.WebResearcher || got.State != agent.Idle {
		t.Fatal(got, err)
	}
	effective := s.Configuration().WebResearcher
	if effective.Prompt.Role != cfg.WebResearcher.Prompt.Role || !effective.InjectedProvider {
		t.Fatal(effective)
	}
	// Researchers read: the shell writes as easily as write_file, and deep
	// research belongs to the deep researcher.
	for _, tool := range effective.Tools {
		switch tool.Name {
		case "shell", "write_file", "edit_file", "deep_research", "get_research_run", "assign_task", "assign_audit", "assign_repair", "submit_work", "submit_audit":
			t.Fatalf("web researcher received %s", tool.Name)
		}
	}
	// Without the web there is no deep research to create a researcher for.
	if _, err = s.CreateAgent(ctx, s.Manager(), roster.CreateRequest{Role: roster.DeepResearcher}); err == nil || !strings.Contains(err.Error(), "use a web_researcher") {
		t.Fatal(err)
	}
}

func TestResearchAssignmentAndListing(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, false)
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(ctx)
	id := createWorker(t, s, roster.WebResearcher)
	w, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.WebResearch, Assignee: id, Task: "Inspect requirements"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListWork(ctx, s.Manager(), work.ListQuery{Kind: work.WebResearch})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != w.ID {
		t.Fatal(page, err)
	}
	if _, err = s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: id, Task: "Change code"}); err == nil {
		t.Fatal("researcher received implementation")
	}
}
