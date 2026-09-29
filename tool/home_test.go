package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Users name folders like ~/Downloads and models pass the path through, so
// the file tools expand ~ as a shell does.
func TestFileToolsExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Downloads", "invoice.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ExpandHome("~user/x"); got != "~user/x" {
		t.Fatal("expanded another user's home:", got)
	}
	f, err := NewFiles(FilesConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]Tool{}
	for _, tl := range f.Tools() {
		tools[tl.Definition().Name] = tl
	}
	call := func(name string, input map[string]any) (string, error) {
		args, _ := json.Marshal(map[string]any{"input": input})
		r, err := tools[name].Call(context.Background(), Call{InvocationID: name, Arguments: args, Actor: "a"})
		return r.Content.Text(), err
	}
	if out, err := call("list_directory", map[string]any{"path": "~/Downloads"}); err != nil || !strings.Contains(out, "invoice.pdf") {
		t.Fatal(out, err)
	}
	if _, err := call("write_file", map[string]any{"path": "~/Downloads/notes.txt", "content": "hi\n"}); err != nil {
		t.Fatal(err)
	}
	if out, err := call("read_file", map[string]any{"path": "~/Downloads/notes.txt", "offset": nil, "limit": nil}); err != nil || !strings.Contains(out, "hi") {
		t.Fatal(out, err)
	}
}
