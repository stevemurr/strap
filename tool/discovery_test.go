package tool

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// discoveryTree builds a small workspace with nested, ignored and binary files.
func discoveryTree(t *testing.T) (string, map[string]Tool) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir()) // results report real paths
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"stock.go":                  "package inventory\n\nfunc CommonStock(a, b []string) []string {\n\treturn nil\n}\n",
		"stock_test.go":             "package inventory\n\nfunc TestCommonStock(t *testing.T) {}\n",
		"cmd/tool/main.go":          "package main\n\nfunc main() {\n\t// TODO wire commonstock\n}\n",
		"web/app.ts":                "export const stock = 1\n",
		"web/view.tsx":              "export const View = () => null\n",
		".git/HEAD":                 "ref: CommonStock\n",
		"node_modules/x/stock.go":   "package x // CommonStock\n",
		"assets/logo.bin":           "Common\x00Stock",
		"docs/README.md":            "Nothing here.\n",
		"docs/deep/nested/stock.go": "package nested\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// stock.go is the most recently changed file.
	old := time.Now().Add(-time.Hour)
	for name := range files {
		if name != "stock.go" {
			os.Chtimes(filepath.Join(dir, name), old, old)
		}
	}
	_, kit := fileTools(t, FilesConfig{Dir: dir})
	return dir, kit
}

func TestDiscoveryToolsValidateInEveryMode(t *testing.T) {
	for _, mode := range []EditMode{EditText, EditAnchors, EditMerge} {
		_, kit := fileTools(t, FilesConfig{Dir: t.TempDir(), Edits: mode})
		for _, name := range []string{"glob", "grep_search", "list_directory"} {
			if kit[name] == nil {
				t.Fatalf("%s mode lacks %s", mode, name)
			}
			if err := ValidateTool(kit[name]); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
	}
}

func TestGlob(t *testing.T) {
	dir, kit := discoveryTree(t)
	got := callText(t, kit["glob"], map[string]any{"pattern": "stock.go", "path": nil})
	// A bare name matches at any depth, newest first; ignored trees are skipped.
	want := "Found 2 file(s) matching \"stock.go\" in " + dir + ", newest first:\n---\n" + filepath.Join(dir, "stock.go") + "\n" + filepath.Join(dir, "docs/deep/nested/stock.go") + "\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	for _, c := range []struct {
		pattern, path string
		want          []string
	}{
		{"**/*.go", "", []string{"stock.go", "stock_test.go", "cmd/tool/main.go", "docs/deep/nested/stock.go"}},
		{"cmd/**/*.go", "", []string{"cmd/tool/main.go"}},
		{"*.{ts,tsx}", "", []string{"web/app.ts", "web/view.tsx"}},
		{"*.go", "cmd", []string{"cmd/tool/main.go"}},
	} {
		var p any
		if c.path != "" {
			p = c.path
		}
		got := callText(t, kit["glob"], map[string]any{"pattern": c.pattern, "path": p})
		if !strings.HasPrefix(got, "Found "+strconv.Itoa(len(c.want))+" file(s)") {
			t.Fatalf("%s: %s", c.pattern, got)
		}
		for _, f := range c.want {
			if !strings.Contains(got, filepath.Join(dir, f)+"\n") {
				t.Fatalf("%s lacks %s:\n%s", c.pattern, f, got)
			}
		}
	}
	if got := callText(t, kit["glob"], map[string]any{"pattern": "*.rs", "path": nil}); !strings.HasPrefix(got, "No files found") {
		t.Fatal(got)
	}
}

func TestGrepSearch(t *testing.T) {
	dir, kit := discoveryTree(t)
	got := callText(t, kit["grep_search"], map[string]any{"pattern": "commonstock", "glob": nil, "path": nil, "limit": nil})
	// Case-insensitive; .git, node_modules and binary files are skipped; lines
	// keep their indentation.
	for _, want := range []string{
		"Found 3 matching line(s)",
		"File: " + filepath.Join(dir, "stock.go") + "\nL3: func CommonStock(a, b []string) []string {\n",
		"File: " + filepath.Join(dir, "cmd/tool/main.go") + "\nL4: \t// TODO wire commonstock\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "node_modules") || strings.Contains(got, ".git") || strings.Contains(got, "logo.bin") {
		t.Fatalf("searched ignored files:\n%s", got)
	}
	if got := callText(t, kit["grep_search"], map[string]any{"pattern": "(?-i)commonstock", "glob": "*.go", "path": nil, "limit": nil}); !strings.Contains(got, "Found 1 matching line(s)") || !strings.Contains(got, "(files matching \"*.go\")") {
		t.Fatalf("case-sensitive with glob:\n%s", got)
	}
	if got := callText(t, kit["grep_search"], map[string]any{"pattern": "package", "glob": nil, "path": nil, "limit": 2}); !strings.Contains(got, "more matching line(s) not shown") {
		t.Fatalf("limit:\n%s", got)
	}
	if got := callText(t, kit["grep_search"], map[string]any{"pattern": "return", "glob": nil, "path": "stock.go", "limit": nil}); !strings.Contains(got, "L4: \treturn nil") {
		t.Fatalf("single file:\n%s", got)
	}
	raw, _ := MarshalInput(map[string]any{"pattern": "(", "glob": nil, "path": nil, "limit": nil})
	if _, err := kit["grep_search"].Call(context.Background(), Call{Arguments: raw}); err == nil || !strings.Contains(err.Error(), "invalid regular expression") {
		t.Fatalf("bad regex: %v", err)
	}
}

func TestListDirectoryAndMissingPaths(t *testing.T) {
	dir, kit := discoveryTree(t)
	got := callText(t, kit["list_directory"], map[string]any{"path": "web"})
	if got != "Listed 2 item(s) in "+filepath.Join(dir, "web")+":\n---\napp.ts\nview.tsx\n" {
		t.Fatal(got)
	}
	if got := callText(t, kit["list_directory"], map[string]any{"path": nil}); !strings.Contains(got, "---\n[DIR] .git\n[DIR] assets\n") {
		t.Fatalf("directories first:\n%s", got)
	}
	raw, _ := MarshalInput(map[string]any{"path": "stock.go"})
	if _, err := kit["list_directory"].Call(context.Background(), Call{Arguments: raw}); err == nil || !strings.Contains(err.Error(), "read it with read_file") {
		t.Fatalf("file as directory: %v", err)
	}
	// The failure that prompted these tools: a guessed directory.
	for _, mode := range []EditMode{EditText, EditMerge} {
		_, kit := fileTools(t, FilesConfig{Dir: dir, Edits: mode})
		raw, _ := MarshalInput(map[string]any{"path": "/workspace-guess/inventory/stock.go", "offset": nil, "limit": nil})
		_, err := kit["read_file"].Call(context.Background(), Call{Arguments: raw})
		if err == nil || err.Error() != `no such file: /workspace-guess/inventory/stock.go; find it with glob, for example pattern "stock.go"` {
			t.Fatalf("%s mode: %v", mode, err)
		}
	}
}
