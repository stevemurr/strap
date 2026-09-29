package harness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func realGopls(t *testing.T) {
	t.Helper()
	if os.Getenv("STRAP_LSP_REAL") != "1" {
		t.Skip("set STRAP_LSP_REAL=1 for installed language servers")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}
}

func goModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"go.mod": "module a\n\ngo 1.24\n",
		"a.go":   "package a\n\nfunc F() int { return 1 }\n",
		"b.go":   "package a\n\nvar _ int = F()\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	real, _ := filepath.EvalSymlinks(dir)
	return real
}

func callText(t *testing.T, tl tool.Tool, actor identity.ActorID, input any) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"input": input})
	started := time.Now()
	r, err := tl.Call(context.Background(), tool.Call{InvocationID: tl.Definition().Name, Arguments: args, Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s took %s", tl.Definition().Name, time.Since(started).Round(time.Millisecond))
	return r.Content.Text()
}

// A write reports the errors in the changed file's package once the server
// has analysed it, and that it is clean once fixed; a file no server covers
// gets nothing.
func TestWriteCheckReportsPackageErrors(t *testing.T) {
	realGopls(t)
	dir := goModule(t)
	cfg := lsp.GoConfig()
	cfg.Dir = dir
	languages, err := lsp.New(cfg, lsp.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer languages.Close(context.Background())
	tools, err := localToolsWithChanges(dir, tool.EditText, languages.Changed, func() { languages.Changed("") })
	if err != nil {
		t.Fatal(err)
	}
	scratch := newScratchLanguages(func(root string) (*lsp.Manager, error) {
		c := lsp.GoConfig()
		c.Dir = root
		return lsp.New(c, lsp.Dependencies{})
	})
	defer scratch.Close(context.Background())
	checkWrites(tools, languages, scratch, dir)
	kit := map[string]tool.Tool{}
	for _, tl := range tools {
		kit[tl.Definition().Name] = tl
	}

	out := callText(t, kit["write_file"], "impl", map[string]any{"path": "c.go", "content": "package a\n\nfunc G() {\n\tx := 1\n}\n"})
	if !strings.Contains(out, "1 error(s)") || !strings.Contains(out, "c.go:4:2 declared and not used: x") {
		t.Fatalf("broken write: %s", out)
	}
	out = callText(t, kit["edit_file"], "impl", map[string]any{"path": "c.go", "old": "\tx := 1\n", "new": "\t_ = 1\n"})
	if !strings.Contains(out, "No errors reported for c.go") {
		t.Fatalf("fixed write: %s", out)
	}
	// A change that breaks another file of the package reports it there.
	out = callText(t, kit["edit_file"], "impl", map[string]any{"path": filepath.Join(dir, "a.go"), "old": "func F() int { return 1 }", "new": "func F() string { return \"\" }"})
	if !strings.Contains(out, "b.go:3:") {
		t.Fatalf("sibling breakage: %s", out)
	}
	out = callText(t, kit["write_file"], "impl", map[string]any{"path": "notes.md", "content": "# notes\n"})
	if strings.Contains(out, "language server") || strings.Contains(out, "No errors") {
		t.Fatalf("a Markdown write was checked: %s", out)
	}
	// A scratch module outside the workspace is checked by a server of its
	// own, and its files are named in full.
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	callText(t, kit["write_file"], "impl", map[string]any{"path": filepath.Join(outside, "go.mod"), "content": "module verify\n\ngo 1.24\n"})
	out = callText(t, kit["write_file"], "impl", map[string]any{"path": filepath.Join(outside, "main.go"), "content": "package main\n\nfunc main() {\n\tundefinedCall()\n}\n"})
	if !strings.Contains(out, filepath.Join(outside, "main.go")+":4:2 undefined: undefinedCall") {
		t.Fatalf("scratch write: %s", out)
	}
}

// A scratch file's project root is the nearest directory with a root marker,
// else its own directory, but never a shared one like the temporary
// directory, whose tree a language server would have to scan.
func TestScratchRoot(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	module := filepath.Join(base, "verify")
	nested := filepath.Join(module, "cmd", "check")
	plain := filepath.Join(base, "plain")
	for _, d := range []string{nested, plain} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module verify\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	temp, _ := filepath.EvalSymlinks(os.TempDir())
	home, _ := os.UserHomeDir()
	for path, want := range map[string]string{
		filepath.Join(nested, "main.go"): module,
		filepath.Join(plain, "a.go"):     plain,
		filepath.Join(temp, "loose.go"):  "",
		filepath.Join(home, "loose.go"):  "",
		"/loose.go":                      "",
	} {
		if got := scratchRoot(path); got != want {
			t.Errorf("scratchRoot(%s) = %q, want %q", path, got, want)
		}
	}
}

// An auditor's write in its copy is checked by the copy's own server, and its
// language tools read the copy, not the real workspace.
func TestWriteCheckInACopy(t *testing.T) {
	realGopls(t)
	dir := goModule(t)
	assignment := work.Work{ID: "work-1", Kind: work.AuditWork, State: work.Active, AssignedAtRevision: 1}
	iso, err := newIsolation(dir, tool.EditText, shellMaxTimeout, func(identity.ActorID) (work.Work, bool, error) {
		return assignment, true, nil
	}, func(work.ID, work.Revision) bool { return true }, func(copyDir string) (*lsp.Manager, error) {
		cfg := lsp.GoConfig()
		cfg.Dir = copyDir
		return lsp.New(cfg, lsp.Dependencies{})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer iso.Close(context.Background())
	templates, err := localToolsWithChanges(dir, tool.EditText, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := lsp.New(func() lsp.Config { c := lsp.GoConfig(); c.Dir = dir; return c }(), lsp.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	languageTools, err := tool.LSPTools(session)
	if err != nil {
		t.Fatal(err)
	}
	kit := map[string]tool.Tool{}
	for _, tl := range iso.tools(append(templates, languageTools...), true) {
		kit[tl.Definition().Name] = tl
	}
	out := callText(t, kit["write_file"], "auditor", map[string]any{"path": filepath.Join(dir, "audit_test.go"), "content": "package a\n\nimport \"testing\"\n\nfunc TestAudit(t *testing.T) {\n\tgot := F()\n}\n"})
	if !strings.Contains(out, "audit_test.go:6:2 declared and not used: got") || strings.Contains(out, "strap-work-1-") {
		t.Fatalf("copy write: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit_test.go")); !os.IsNotExist(err) {
		t.Fatal("the auditor's write reached the workspace")
	}
	out = callText(t, kit["lsp_diagnostics"], "auditor", map[string]any{"paths": []string{filepath.Join(dir, "audit_test.go")}, "cursor": nil, "limit": nil})
	if !strings.Contains(out, "declared and not used") {
		t.Fatalf("the auditor's language tools do not see its copy: %s", out)
	}
}

// writeTargets finds what a command writes, following cd, and never reads
// heredoc bodies, where > is file text.
func TestWriteTargets(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	scratch := filepath.Join(base, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	command := "cd " + scratch + " && cat > main.go << 'EOF'\npackage main\nfunc f(a, b int) bool { return a > b }\nEOF\n" +
		"go run . > out.txt 2>&1; echo hi | tee -a " + filepath.Join(base, "log.go") + " >/dev/null\n" +
		"cp " + filepath.Join(base, "a.go") + " " + scratch + " && mv x.go ../y.go\n" +
		"cat > " + filepath.Join(base, "second.go") + " <<GOEOF\nx >> nowhere.go\nGOEOF"
	got := writeTargets(command, "/workspace")
	want := []string{
		filepath.Join(scratch, "main.go"),
		filepath.Join(scratch, "out.txt"),
		filepath.Join(base, "log.go"),
		filepath.Join(scratch, "a.go"),
		filepath.Join(base, "y.go"),
		filepath.Join(base, "second.go"),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("writeTargets:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A shell command that writes Go files gets the same report as write_file:
// inside the workspace from the before/after comparison, outside it from the
// command's own redirections. A command that changes no such file gets none.
func TestShellCheckReportsChangedPackages(t *testing.T) {
	realGopls(t)
	dir := goModule(t)
	cfg := lsp.GoConfig()
	cfg.Dir = dir
	languages, err := lsp.New(cfg, lsp.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer languages.Close(context.Background())
	scratch := newScratchLanguages(func(root string) (*lsp.Manager, error) {
		c := lsp.GoConfig()
		c.Dir = root
		return lsp.New(c, lsp.Dependencies{})
	})
	defer scratch.Close(context.Background())
	tools, err := localToolsWithChanges(dir, tool.EditText, languages.Changed, func() { languages.Changed("") })
	if err != nil {
		t.Fatal(err)
	}
	checkShell(tools, languages, scratch, dir)
	var shell tool.Tool
	for _, tl := range tools {
		if tl.Definition().Name == "shell" {
			shell = tl
		}
	}
	run := func(command string) string {
		return callText(t, shell, "impl", map[string]any{"command": command, "timeout_ms": nil})
	}
	out := run("cat > c.go << 'EOF'\npackage a\n\nfunc G() {\n\tx := 1\n}\nEOF")
	if !strings.Contains(out, "This command changed c.go.") || !strings.Contains(out, "c.go:4:2 declared and not used: x") {
		t.Fatalf("heredoc in the workspace: %s", out)
	}
	if out = run("ls && echo notes > notes.md"); strings.Contains(out, "This command changed") {
		t.Fatalf("a command that changed no Go file was checked: %s", out)
	}
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	out = run("cd " + outside + " && printf 'module verify\\n\\ngo 1.24\\n' > go.mod && cat > main.go << 'EOF'\npackage main\n\nfunc main() {\n\tundefinedCall()\n}\nEOF")
	if !strings.Contains(out, filepath.Join(outside, "main.go")+":4:2 undefined: undefinedCall") {
		t.Fatalf("heredoc outside the workspace: %s", out)
	}
}

// A shell result keeps being one JSON object with the diagnostics as a field,
// its fields in their order; other output gets the text after it.
func TestWithDiagnostics(t *testing.T) {
	shell := `{"started":true,"output":"ok\n","exit_code":0}`
	got := withDiagnostics(content.Text(shell), "This command changed c.go.\nNo errors reported for c.go or the other files of its package.").Text()
	var parsed struct {
		ExitCode    *int   `json:"exit_code"`
		Diagnostics string `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil || parsed.ExitCode == nil || *parsed.ExitCode != 0 || !strings.HasPrefix(parsed.Diagnostics, "This command changed c.go.") || !strings.HasPrefix(got, `{"started":true,"output":"ok\n","exit_code":0,`) {
		t.Fatalf("spliced: %s %v", got, err)
	}
	if got := withDiagnostics(content.Text("plain output"), "report").Text(); got != "plain outputreport" && !strings.Contains(got, "report") {
		t.Fatalf("appended: %q", got)
	}
}

// goImportDirs maps a Go file's imports to directories: its own module's
// packages and modules its go.mod replaces with a local directory.
func TestGoImportDirs(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	ws := filepath.Join(base, "ws")
	scratch := filepath.Join(base, "scratch")
	files := map[string]string{
		filepath.Join(ws, "go.mod"):              "module tags\n\ngo 1.24\n",
		filepath.Join(ws, "tags.go"):             "package tags\n",
		filepath.Join(ws, "sub", "sub.go"):       "package sub\n",
		filepath.Join(ws, "cmd", "c", "main.go"): "package main\n\nimport (\n\t\"fmt\"\n\t\"tags\"\n\t\"tags/sub\"\n\t\"tagsother\"\n)\n",
		filepath.Join(scratch, "go.mod"):         "module verify\n\ngo 1.24\n\nrequire tags v0.0.0\n\nreplace tags => " + ws + "\n",
		filepath.Join(scratch, "main.go"):        "package main\n\nimport \"tags\"\n",
	}
	for path, text := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := goImportDirs(filepath.Join(ws, "cmd", "c", "main.go")); strings.Join(got, ",") != ws+","+filepath.Join(ws, "sub") {
		t.Errorf("module imports: %v", got)
	}
	if got := goImportDirs(filepath.Join(scratch, "main.go")); strings.Join(got, ",") != ws {
		t.Errorf("replaced module: %v", got)
	}
	if got := goImportDirs(filepath.Join(ws, "tags.go")); len(got) != 0 {
		t.Errorf("no imports: %v", got)
	}
}

// A file that imports a workspace package is reported broken when that
// package is, though its own package checks clean.
func TestWriteCheckReportsImportedPackageErrors(t *testing.T) {
	realGopls(t)
	dir := goModule(t)
	cfg := lsp.GoConfig()
	cfg.Dir = dir
	languages, err := lsp.New(cfg, lsp.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer languages.Close(context.Background())
	tools, err := localToolsWithChanges(dir, tool.EditText, languages.Changed, func() { languages.Changed("") })
	if err != nil {
		t.Fatal(err)
	}
	checkWrites(tools, languages, nil, dir)
	kit := map[string]tool.Tool{}
	for _, tl := range tools {
		kit[tl.Definition().Name] = tl
	}
	main := map[string]any{"path": "cmd/check/main.go", "content": "package main\n\nimport \"a\"\n\nfunc main() { _ = a.F() }\n"}
	out := callText(t, kit["write_file"], "impl", main)
	if !strings.Contains(out, "No errors reported for cmd/check/main.go, the other files of its package or the workspace packages it imports.") {
		t.Fatalf("clean import: %s", out)
	}
	callText(t, kit["write_file"], "impl", map[string]any{"path": "stray.go", "content": "package main\n\nfunc main() {}\n"})
	out = callText(t, kit["write_file"], "impl", main)
	if !strings.Contains(out, "error(s)") || !strings.Contains(out, "stray.go") {
		t.Fatalf("broken import: %s", out)
	}
}
