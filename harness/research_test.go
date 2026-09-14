package harness_test

import (
	"context"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/roster"
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
