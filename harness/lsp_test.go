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
	for _, role := range []harness.RoleConfiguration{effective.Root, effective.Implementor, effective.Auditor, effective.Researcher} {
		count := 0
		for _, tool := range role.Tools {
			if strings.HasPrefix(tool.Name, "lsp_") {
				count++
			}
		}
		if count != 7 {
			t.Fatalf("role has %d language tools", count)
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
		for i, role := range []harness.AgentConfig{assembled.Root, assembled.Implementor, assembled.Auditor, assembled.Researcher} {
			instructions := strings.Join(role.Prompt.Instructions, "\n")
			// The root never creates files or changes code, so it gets neither guidance.
			root := i == 0
			if strings.Count(instructions, "For existing files, copy paths exactly from the user or tool results.") != 1 ||
				strings.Contains(instructions, "New files may use new paths.") == root {
				t.Fatalf("enabled=%v role %d: wrong shared file rule", enabled, i)
			}
			if strings.Contains(instructions, "You never write, edit or create workspace files") != root {
				t.Fatalf("enabled=%v role %d: only the root is told it never changes files", enabled, i)
			}
			if enabled && strings.Contains(instructions, "After changing code") == root {
				t.Fatalf("enabled=%v role %d: wrong language guidance", enabled, i)
			}
		}
		effective := s.Configuration()
		for _, role := range []harness.RoleConfiguration{effective.Root, effective.Implementor, effective.Auditor, effective.Researcher} {
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
