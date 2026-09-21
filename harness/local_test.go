package harness

import "testing"

func TestLocalToolsIncludesShellAndFiles(t *testing.T) {
	tools, err := localToolsWithChanges(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"shell": true, "read_file": true, "write_file": true, "edit_file": true, "read_pdf": true}
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
