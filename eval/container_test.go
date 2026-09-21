package eval_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// Run only in eval/Dockerfile's smoke target; ordinary tests never touch root mounts.
func TestContainerWorkflow(t *testing.T) {
	if os.Getenv("STRAP_EVAL_CONTAINER_SMOKE") != "1" {
		t.Skip("container smoke target only")
	}
	exerciseMountedWorkflow(t, eval.ContainerMounts())
}

func TestMountedToolPathsWithGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	ladder, err := filepath.Abs("ladder")
	if err != nil {
		t.Fatal(err)
	}
	exerciseMountedWorkflow(t, eval.Mounts{Workspace: t.TempDir(), Results: t.TempDir(), Outbox: t.TempDir(), Problems: publicProblems(t, ladder), Grading: ladder})
}

func exerciseMountedWorkflow(t *testing.T, mounts eval.Mounts) {
	t.Helper()
	for _, private := range []string{"hidden", "reference"} {
		if _, err := os.Stat(filepath.Join(mounts.Problems, "easy", "01-budget-pair", private)); !os.IsNotExist(err) {
			t.Fatal("private fixture in public image", err)
		}
	}
	solution, err := os.ReadFile(filepath.Join(mounts.Grading, "easy/01-budget-pair/reference/budget.go"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.DefaultConfig()
	languages := lsp.DefaultConfig()
	cfg.LSP = &languages
	canonical, err := filepath.EvalSymlinks(mounts.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	p := &containerScript{solution: string(solution), workspace: canonical}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := eval.Run(ctx, eval.Options{Config: cfg, Deps: harness.Dependencies{Provider: p}, Mounts: mounts, Problem: "easy-01-budget-pair", Quiet: time.Millisecond})
	if err != nil || len(results) != 1 || results[0].Outcome != eval.Submitted || p.calls != 6 {
		t.Fatal(results, p.calls, err)
	}
	if results[0].Workspace != canonical {
		t.Fatal(results[0].Workspace)
	}
	// Simulate the fresh grader container's empty /workspace. The handoff must
	// remain sufficient after all agent working files have gone away.
	entries, err := os.ReadDir(mounts.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(mounts.Workspace, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	r, err := eval.GradeSubmission(ctx, mounts)
	if err != nil || !r.Passed {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(filepath.Join(mounts.Outbox, "submission/workspace/budget_hidden_test.go")); !os.IsNotExist(err) {
		t.Fatal("grader wrote into outbox", err)
	}
}

type containerScript struct {
	calls     int
	solution  string
	workspace string
}

func (p *containerScript) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls > 5 {
		return provider.Response{}, fmt.Errorf("unexpected extra model call")
	}
	if p.calls > 0 {
		last := r.Messages[len(r.Messages)-1]
		if last.Role != "tool" || strings.Contains(last.Content.Text(), "Tool error:") {
			return provider.Response{}, fmt.Errorf("unexpected tool receipt: %s", last.Content.Text())
		}
		want := []string{"", fmt.Sprintf(`"output":%q`, p.workspace+"\n"), `"path":"budget.go"`, fmt.Sprintf(`"path":%q`, filepath.Join(p.workspace, "README.md")), "PairForBudget", `"path":"budget.go"`}[p.calls]
		if !strings.Contains(last.Content.Text(), want) {
			return provider.Response{}, fmt.Errorf("receipt lacks %q: %s", want, last.Content.Text())
		}
	}
	p.calls++
	call := func(name string, args map[string]any) (provider.Response, error) {
		input, err := tool.MarshalInput(args)
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("call-%d", p.calls), Name: name, Arguments: input}}}, err
	}
	switch p.calls {
	case 1:
		return call("shell", map[string]any{"command": "pwd -P", "timeout_ms": nil})
	case 2:
		return call("read_file", map[string]any{"path": "budget.go", "offset": nil, "limit": nil})
	case 3:
		return call("read_file", map[string]any{"path": filepath.Join(p.workspace, "README.md"), "offset": nil, "limit": nil})
	case 4:
		return call("lsp_outline", map[string]any{"path": "budget.go", "depth": nil, "limit": nil, "cursor": nil})
	case 5:
		return call("write_file", map[string]any{"path": "budget.go", "content": p.solution})
	default:
		return provider.Response{Content: "Finished."}, nil
	}
}
