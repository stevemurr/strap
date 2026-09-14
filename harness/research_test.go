package harness_test

import (
	"context"
	"encoding/json"
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
	foundShell := false
	for _, tool := range effective.Tools {
		if tool.Name == "shell" {
			foundShell = true
			var params map[string]any
			if err := json.Unmarshal(tool.Parameters, &params); err != nil {
				t.Fatal(err)
			}
			raw := string(tool.Parameters)
			if !strings.Contains(raw, "assigned_at_revision") || !strings.Contains(raw, "60000") {
				t.Fatal(raw)
			}
		}
		switch tool.Name {
		case "write_file", "edit_file", "assign_work", "submit_work", "submit_audit":
			t.Fatalf("researcher received %s", tool.Name)
		}
	}
	if !foundShell {
		t.Fatal("researcher diagnostic shell missing")
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

func TestResearchExecutionConfigurationIsDetached(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.ResearchExecution.Env = []string{"RESEARCH_VALUE=original"}
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: idle{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	cfg.ResearchExecution.Env[0] = "RESEARCH_VALUE=caller"
	snapshot := s.Configuration()
	snapshot.ResearchExecution.Env[0] = "RESEARCH_VALUE=reader"
	if got := s.Configuration().ResearchExecution.Env[0]; got != "RESEARCH_VALUE=original" {
		t.Fatal(got)
	}
}
