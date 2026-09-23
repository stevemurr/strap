package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stevemurr/strap/tool"
)

func TestLocalToolsIncludesShellAndFiles(t *testing.T) {
	tools, err := localToolsWithChanges(t.TempDir(), "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"shell": true, "read_file": true, "write_file": true, "edit_file": true, "read_pdf": true, "glob": true, "grep_search": true, "list_directory": true}
	for _, operation := range tools {
		name := operation.Definition().Name
		if !want[name] {
			t.Fatalf("unexpected or duplicate tool %s", name)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tools: %v", want)
	}
}

// Container runs receive the harness config as JSON, so the edit mode must
// survive a round trip and select the line-label contract.
func TestFileEditsModeSelectsLineLabelContract(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FileEdits = tool.EditAnchors
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.FileEdits != tool.EditAnchors {
		t.Fatalf("round trip: %q, %v", decoded.FileEdits, err)
	}
	tools, err := localToolsWithChanges(t.TempDir(), decoded.FileEdits, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range tools {
		if d := operation.Definition(); d.Name == "edit_file" && !strings.Contains(string(d.Parameters), `"start"`) {
			t.Fatalf("edit_file still uses text matching: %s", d.Parameters)
		}
	}
	if _, err := localToolsWithChanges(t.TempDir(), "fuzzy", nil, nil); err == nil {
		t.Fatal("unknown edit mode accepted")
	}
}
