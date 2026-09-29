package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/lsp"
)

func TestLanguageAssembly(t *testing.T) {
	cfg := testConfig(t, true)
	languages := lsp.GoConfig()
	languages.Servers[0].Command = []string{"missing-language-server"}
	languages.Servers[0].Env = map[string]string{"SECRET": "never-record-this"}
	cfg.LSP = &languages
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	effective := s.Configuration()
	for i, role := range []harness.RoleConfiguration{effective.Implementor, effective.Auditor, *effective.Reviewer, *effective.Experimenter} {
		count := 0
		for _, tool := range role.Tools {
			if strings.HasPrefix(tool.Name, "lsp_") {
				count++
			}
		}
		if want := 7; count != want {
			t.Fatalf("role %d has %d language tools, want %d", i, count, want)
		}
	}
	raw, _ := json.Marshal(effective)
	if strings.Contains(string(raw), "never-record-this") {
		t.Fatal("configuration leaked environment")
	}
	languages.Servers[0].Env["SECRET"] = "mutated"
	copy := s.Config()
	copy.LSP.Servers[0].Command[0] = "mutated"
	if s.Config().LSP.Servers[0].Env["SECRET"] != "never-record-this" || s.Config().LSP.Servers[0].Command[0] == "mutated" {
		t.Fatal("config aliases caller")
	}
	effective.LSP.Servers[0] = "mutated"
	if s.Configuration().LSP.Servers[0] == "mutated" {
		t.Fatal("effective config aliases caller")
	}
}

func TestDefaultLanguageToolsAndOptOut(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := testConfig(t, true)
		if !enabled {
			cfg.LSP = nil
		}
		s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("ready")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		assembled := s.Config()
		for i, role := range []harness.AgentConfig{assembled.Implementor, assembled.Auditor, assembled.Reviewer, assembled.Experimenter} {
			instructions := strings.Join(role.Prompt.Instructions, "\n")
			if strings.Count(instructions, "For existing files, copy paths exactly from the user or tool results.") != 1 ||
				!strings.Contains(instructions, "New files may use new paths.") {
				t.Fatalf("enabled=%v role %d: wrong shared file rule", enabled, i)
			}
			if enabled && !strings.Contains(instructions, "After changing code") {
				t.Fatalf("enabled=%v role %d: wrong language guidance", enabled, i)
			}
		}
		effective := s.Configuration()
		for _, role := range []harness.RoleConfiguration{effective.Implementor, effective.Auditor, *effective.Reviewer, *effective.Experimenter} {
			count := 0
			for _, tool := range role.Tools {
				if strings.HasPrefix(tool.Name, "lsp_") {
					count++
				}
			}
			want := 0
			if enabled {
				want = 7
			}
			if count != want {
				t.Fatalf("enabled=%v: role has %d language tools, want %d", enabled, count, want)
			}
		}
		if enabled && len(effective.LSP.Servers) != 5 {
			t.Fatalf("default presets: %+v", effective.LSP)
		}
	}
}
