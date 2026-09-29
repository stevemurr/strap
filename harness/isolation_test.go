package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// isolatedFixture is a workspace with one file and an isolation whose actor
// holds audit work-1 until release is called.
type isolatedFixture struct {
	dir   string
	iso   *isolation
	tools map[string]tool.Tool // an experimenter's: writes stay in the copy
	// auditorTools may also write outside the copy.
	auditorTools map[string]tool.Tool
	held         bool
}

func newIsolatedFixture(t *testing.T) *isolatedFixture {
	t.Helper()
	f := &isolatedFixture{dir: t.TempDir(), held: true}
	if err := os.WriteFile(filepath.Join(f.dir, "text.go"), []byte("package text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assignment := work.Work{ID: "work-1", Kind: work.AuditWork, State: work.Active, AssignedAtRevision: 1}
	iso, err := newIsolation(f.dir, tool.EditText, shellMaxTimeout, func(actor identity.ActorID) (work.Work, bool, error) {
		return assignment, f.held && actor == "auditor", nil
	}, func(work.ID, work.Revision) bool { return f.held }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = iso.Close(context.Background()) })
	templates, err := localToolsWithChanges(f.dir, tool.EditText, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range templates {
		if tl.Definition().Name == "shell" {
			templates = append(templates, trialsTool(tl, shellMaxTimeout))
		}
	}
	f.iso, f.tools, f.auditorTools = iso, map[string]tool.Tool{}, map[string]tool.Tool{}
	for _, tl := range iso.tools(templates, false) {
		f.tools[tl.Definition().Name] = tl
	}
	for _, tl := range iso.tools(templates, true) {
		f.auditorTools[tl.Definition().Name] = tl
	}
	return f
}

func (f *isolatedFixture) call(t *testing.T, name string, input any) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.tools[name].Call(context.Background(), tool.Call{InvocationID: name, Arguments: args, Actor: "auditor"})
	return r.Content.Text(), err
}

// The copy is transparent: the model names workspace paths and sees them in
// results, while every change lands in the copy.
func TestIsolatedToolsWorkOnACopyUnderTheWorkspacePaths(t *testing.T) {
	f := newIsolatedFixture(t)
	real, _ := filepath.EvalSymlinks(f.dir)
	written := filepath.Join(f.dir, "text_test.go")
	if _, err := f.call(t, "write_file", map[string]any{"path": written, "content": "package text\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(written); !os.IsNotExist(err) {
		t.Fatal("an isolated write reached the workspace:", err)
	}
	out, err := f.call(t, "shell", map[string]any{"command": "pwd; ls " + f.dir + "; echo changed > text.go", "timeout_ms": nil})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, real) || !strings.Contains(out, "text_test.go") || strings.Contains(out, "strap-work-1") {
		t.Fatal("the shell did not see the copy under the workspace path:", out)
	}
	if data, _ := os.ReadFile(filepath.Join(f.dir, "text.go")); string(data) != "package text\n" {
		t.Fatal("a shell write reached the workspace:", string(data))
	}
	read, err := f.call(t, "read_file", map[string]any{"path": "text.go", "offset": nil, "limit": nil})
	if err != nil || !strings.Contains(read, "changed") {
		t.Fatal("the copy did not keep the shell's write:", read, err)
	}
	// Without an assignment there is no copy to act on.
	f.held = false
	if _, err = f.call(t, "read_file", map[string]any{"path": "text.go", "offset": nil, "limit": nil}); err == nil || !strings.Contains(err.Error(), "own copy") {
		t.Fatal(err)
	}
}

func TestRunTrialsTimesEachRun(t *testing.T) {
	f := newIsolatedFixture(t)
	out, err := f.call(t, "run_trials", map[string]any{"command": "echo ran >> trials.log; cat trials.log | wc -l", "trials": 3, "timeout_ms": nil})
	if err != nil {
		t.Fatal(err)
	}
	var r tool.TrialsResult
	if err = json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(out, err)
	}
	if len(r.Trials) != 3 || r.Failed != 0 || strings.TrimSpace(r.LastOutput) != "3" || r.MinMS > r.MedianMS || r.MedianMS > r.MaxMS {
		t.Fatal(out)
	}
	if _, err = os.Stat(filepath.Join(f.dir, "trials.log")); !os.IsNotExist(err) {
		t.Fatal("trials ran in the workspace:", err)
	}
}

func TestMethodFilesComeFromTheCopyAndItEndsWithTheAssignment(t *testing.T) {
	f := newIsolatedFixture(t)
	if _, err := f.call(t, "write_file", map[string]any{"path": "bench/render_test.go", "content": "package bench\n"}); err != nil {
		t.Fatal(err)
	}
	files, err := f.iso.methodFiles("auditor", []string{filepath.Join(f.dir, "bench/render_test.go"), "text.go"})
	if err != nil || len(files) != 2 || files[0].Path != "bench/render_test.go" || files[0].Content != "package bench\n" {
		t.Fatal(files, err)
	}
	for _, outside := range []string{"../escape.go", "/etc/hosts"} {
		if _, err = f.iso.methodFiles("auditor", []string{outside}); err == nil {
			t.Fatal("read a method file outside the workspace:", outside)
		}
	}
	var copyDir string
	for _, c := range f.iso.copies {
		copyDir = c.dir
	}
	f.held = false
	if _, err = f.iso.copyFor(work.Work{ID: "work-2", AssignedAtRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(copyDir); !os.IsNotExist(err) {
		t.Fatal("an ended assignment's copy was kept:", err)
	}
}

// An auditor may write outside its copy, such as a scratch program in /tmp,
// while its writes to workspace paths still land in the copy.
func TestAuditorWritesOutsideTheWorkspace(t *testing.T) {
	f := newIsolatedFixture(t)
	outside := t.TempDir()
	call := func(name string, input any) error {
		args, _ := json.Marshal(map[string]any{"input": input})
		_, err := f.auditorTools[name].Call(context.Background(), tool.Call{InvocationID: name, Arguments: args, Actor: "auditor"})
		return err
	}
	scratch := filepath.Join(outside, "main.go")
	if err := call("write_file", map[string]any{"path": scratch, "content": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if err := call("edit_file", map[string]any{"path": scratch, "old": "main", "new": "scratch"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(scratch); string(data) != "package scratch\n" {
		t.Fatalf("outside file holds %q", data)
	}
	if err := call("write_file", map[string]any{"path": filepath.Join(f.dir, "audit_test.go"), "content": "package text\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "audit_test.go")); !os.IsNotExist(err) {
		t.Fatal("an auditor's workspace write reached the real workspace:", err)
	}
}

// Only the workspace is copied: a folder elsewhere is the real one, so an
// experimenter reads it but its writes there are refused.
func TestIsolatedToolsReadButNeverWriteOutsideTheWorkspace(t *testing.T) {
	f := newIsolatedFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "photo.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := f.call(t, "list_directory", map[string]any{"path": outside}); err != nil || !strings.Contains(out, "photo.jpg") {
		t.Fatal("an outside folder could not be read:", out, err)
	}
	target := filepath.Join(outside, "notes.txt")
	for _, name := range []string{"write_file", "edit_file"} {
		input := map[string]any{"path": target, "content": "x"}
		if name == "edit_file" {
			input = map[string]any{"path": filepath.Join(outside, "photo.jpg"), "old": "x", "new": "y"}
		}
		if _, err := f.call(t, name, input); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
			t.Fatalf("%s outside the workspace: %v", name, err)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a write reached the outside folder:", err)
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "photo.jpg")); string(data) != "x" {
		t.Fatal("an edit reached the outside folder:", string(data))
	}
}
