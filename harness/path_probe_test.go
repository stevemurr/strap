package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func TestPathComparisonChangesOnlyDescriptions(t *testing.T) {
	raw, err := os.ReadFile("testdata/path-tools-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var original []provider.ToolDefinition
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	ts, err := localTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := tool.LSPTools(new(lsp.Manager))
	if err != nil {
		t.Fatal(err)
	}
	ts = append(ts, ls...)
	old := map[string]provider.ToolDefinition{}
	for _, d := range original {
		if strings.Contains(d.Description, "/privateWORKSPACE_ROOT") {
			t.Fatal("baseline root must be a whole placeholder")
		}
		old[d.Name] = d
	}
	for _, item := range ts {
		current := item.Definition()
		previous, ok := old[current.Name]
		if !ok {
			t.Fatalf("baseline lacks %s", current.Name)
		}
		var a, b any
		if err := json.Unmarshal(current.Parameters, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(previous.Parameters, &b); err != nil {
			t.Fatal(err)
		}
		stripPathDescriptions(a)
		stripPathDescriptions(b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s changed schema beyond descriptions", current.Name)
		}
		delete(old, current.Name)
	}
	if len(old) != 0 {
		t.Fatalf("baseline has extra tools: %v", old)
	}
}
func stripPathDescriptions(v any) {
	switch v := v.(type) {
	case map[string]any:
		delete(v, "description")
		for _, child := range v {
			stripPathDescriptions(child)
		}
	case []any:
		for _, child := range v {
			stripPathDescriptions(child)
		}
	}
}

func TestPathProbeRejectsWrongFileSuccess(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	for _, name := range []string{"prefix.go", "config/prefix.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package config\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tc := pathCase{name: "package_is_not_directory", wantTool: "read_file", wantPath: "prefix.go"}
	for _, test := range []struct {
		path           string
		correct, wrong bool
	}{{"prefix.go", true, false}, {filepath.Join(dir, "prefix.go"), true, false}, {"config/prefix.go", false, true}} {
		args, _ := json.Marshal(map[string]any{"input": map[string]string{"path": test.path}})
		correct, wrong := scorePathCall(dir, tc, provider.ToolCall{Name: "read_file", Arguments: args}, `{"content":"1\tpackage config\n"}`, nil)
		if correct != test.correct || wrong != test.wrong {
			t.Fatalf("path=%s correct=%v wrong=%v", test.path, correct, wrong)
		}
	}
}

func TestPathProbeAcceptsEquivalentSourceInspection(t *testing.T) {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	tc := pathCase{name: "conflicting_handoff", wantTool: "read_file", wantPath: "prefix.go"}
	for _, row := range []struct {
		path           string
		correct, wrong bool
	}{{"prefix.go", true, false}, {"config/prefix.go", false, true}, {"README.md", false, false}} {
		args, _ := json.Marshal(map[string]any{"input": map[string]string{"path": row.path}})
		correct, wrong := scorePathCall(dir, tc, provider.ToolCall{Name: "lsp_inspect", Arguments: args}, `{"location":{"excerpt":"func SharedPrefix() {}"}}`, nil)
		if correct != row.correct || wrong != row.wrong {
			t.Fatalf("%s: correct=%v wrong=%v", row.path, correct, wrong)
		}
	}
}

func TestFrozenPathCandidatesChangeOnlyDescriptions(t *testing.T) {
	load := func(path string) map[string]provider.ToolDefinition {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var definitions []provider.ToolDefinition
		if err := json.Unmarshal(b, &definitions); err != nil {
			t.Fatal(err)
		}
		out := map[string]provider.ToolDefinition{}
		for _, d := range definitions {
			out[d.Name] = d
		}
		return out
	}
	baseline := load("testdata/path-tools-baseline.json")
	candidate := load("testdata/path-tools-candidate.json")
	if len(baseline) != len(candidate) {
		t.Fatal("tool rosters differ")
	}
	for name, old := range baseline {
		current, ok := candidate[name]
		if !ok {
			t.Fatalf("candidate lacks %s", name)
		}
		var a, b any
		if err := json.Unmarshal(old.Parameters, &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(current.Parameters, &b); err != nil {
			t.Fatal(err)
		}
		stripPathDescriptions(a)
		stripPathDescriptions(b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s schema changed beyond wording", name)
		}
	}
}
