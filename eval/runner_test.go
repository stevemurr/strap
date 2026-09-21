package eval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/tool"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
)

// script drives the root agent without a model: optionally write the solution
// with the write_file tool, then reply.
type script struct {
	calls   atomic.Int32
	write   bool
	block   bool
	wait    bool  // Delegate to a worker that never finishes, then wait on it.
	fail    error // Returned from every Submit, like an unreachable server.
	content string
}

func (p *script) Submit(ctx context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.block {
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	}
	if p.fail != nil {
		return provider.Response{}, p.fail
	}
	n := p.calls.Add(1)
	if p.write && n == 1 {
		args, _ := tool.MarshalInput(map[string]string{"path": "probe.go", "content": p.content})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "write_file", Arguments: args}}}, nil
	}
	if p.wait {
		return p.delegate(r, n)
	}
	return provider.Response{Content: "Done: Answer returns 42."}, nil
}

// delegate scripts a root that creates an implementor, assigns it work and
// waits, and an implementor that only acknowledges. The session then idles
// with live work and no root reply, which is what the runner's idle rule is
// for now that a root without live work cannot wait.
func (p *script) delegate(r provider.Request, n int32) (provider.Response, error) {
	if r.Agent != "agent-1" {
		return provider.Response{Content: "Working on it."}, nil
	}
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("call-%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 2:
		return call("create_agent", `{"input":{"role":"implementor"}}`), nil
	case 3:
		assignee := "agent-2"
		for i := len(r.Messages) - 1; i >= 0; i-- {
			if r.Messages[i].Role != "tool" {
				continue
			}
			if m := regexp.MustCompile(`"agent_id":"([^"]+)"`).FindStringSubmatch(r.Messages[i].Content.Text()); m != nil {
				assignee = m[1]
			}
			break
		}
		return call("assign_implementation", fmt.Sprintf(`{"input":{"assignee":%q,"task":"Implement Answer in probe.go","context":null,"expected_output":null,"scope":null}}`, assignee)), nil
	default:
		return provider.Response{Content: "Waiting for the implementor.", ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("wait-%d", n), Name: "wait_for_input", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
	}
}

func writeLadder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "easy", "00-probe")
	files := map[string]string{
		"task.json":                   `{"id":"easy-00-probe","tier":"easy","title":"Probe","insight":"none","prompt":"Implement Answer in probe.go so it returns 42.","timeout":"4s","test_timeout":"1m"}`,
		"workspace/go.mod":            "module probe\n\ngo 1.24\n",
		"workspace/probe.go":          "package probe\n\nfunc Answer() int { panic(\"not implemented\") }\n",
		"hidden/probe_hidden_test.go": "package probe\n\nimport \"testing\"\n\nfunc TestHiddenAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(Answer())\n\t}\n}\n",
		"reference/probe.go":          "package probe\n\nfunc Answer() int { return 42 }\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func options(t *testing.T, ladder string, p provider.Provider) eval.Options {
	t.Helper()
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	return eval.Options{Config: cfg, Deps: harness.Dependencies{Provider: p}, Mounts: eval.Mounts{Problems: publicProblems(t, ladder), Grading: ladder, Results: t.TempDir(), Workspace: t.TempDir(), Outbox: t.TempDir()}, Problem: "easy-00-probe", Log: testWriter{t}, Quiet: 200 * time.Millisecond}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func TestRunPassesAndRetainsMounts(t *testing.T) {
	ladder := writeLadder(t)
	p := &script{write: true, content: "package probe\n\nfunc Answer() int { return 42 }\n"}
	opts := options(t, ladder, p)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := runAndGrade(t, ctx, opts)
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	r := results[0]
	if r.Outcome != eval.Passed || !r.Passed || r.TimedOut || r.Replies != 1 || r.Error != "" || r.Grade == nil || !r.Grade.Compiled {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Reply, "42") {
		t.Fatalf("reply %q", r.Reply)
	}
	reader, err := eventlog.OpenJSONL(ctx, r.Trace)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Read(ctx, eventlog.Query{Limit: 1000})
	reader.Close(ctx)
	if err != nil || !page.Sealed || len(page.Events) < 5 {
		t.Fatalf("trace: sealed=%v events=%d err=%v", page.Sealed, len(page.Events), err)
	}
	if _, err := os.Stat(filepath.Join(opts.Mounts.Workspace, "probe_hidden_test.go")); !os.IsNotExist(err) {
		t.Fatal("hidden test leaked into agent workspace:", err)
	}
	// The advertised workspace is the actual mount and stays in place after grading.
	trace, _ := os.ReadFile(r.Trace)
	canonical, err := filepath.EvalSymlinks(opts.Mounts.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if r.Workspace != canonical || !strings.Contains(string(trace), canonical) {
		t.Fatalf("workspace mismatch: %q", r.Workspace)
	}
	if _, err := os.Stat(filepath.Join(opts.Mounts.Results, r.TaskID, "workspace")); !os.IsNotExist(err) {
		t.Fatal("workspace was copied into results", err)
	}
	lines, _ := os.ReadFile(filepath.Join(opts.Mounts.Results, "results.jsonl"))
	if strings.Count(string(lines), "\n") != 1 {
		t.Fatalf("results.jsonl: %q", lines)
	}
	rep, err := eval.Analyze(ctx, opts.Mounts.Results)
	if err != nil || len(rep.Tasks) != 1 {
		t.Fatal(rep, err)
	}
	m := rep.Tasks[0]
	if !m.Passed || m.Agents != 1 || m.Replies != 1 || m.ToolCalls["write_file"] != 1 || m.ModelCalls < 2 || m.Error != "" {
		t.Fatalf("%+v", m)
	}
	if md := rep.Markdown(); !strings.Contains(md, "easy-00-probe") || !strings.Contains(md, "100%") {
		t.Fatal(md)
	}
	if err := eval.WriteReport(rep); err != nil {
		t.Fatal(err)
	}
	// A new attempt cannot silently reuse or overwrite existing mounted data.
	calls := p.calls.Load()
	again, err := runAndGrade(t, ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "must be empty") || len(again) != 0 || p.calls.Load() != calls {
		t.Fatal(again, err, p.calls.Load(), calls)
	}

}

func TestRunGradesFailure(t *testing.T) {
	ladder := writeLadder(t)
	opts := options(t, ladder, &script{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := runAndGrade(t, ctx, opts)
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	r := results[0]
	if r.Outcome != eval.Failed || r.Passed || r.TimedOut || r.Replies != 1 || r.Grade == nil || !r.Grade.Compiled || r.Grade.Passed {
		t.Fatalf("%+v", r)
	}
}

func TestRunBudgetExhausted(t *testing.T) {
	ladder := writeLadder(t)
	opts := options(t, ladder, &script{block: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := runAndGrade(t, ctx, opts)
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	r := results[0]
	if !r.TimedOut || r.Outcome != eval.Failed || r.Replies != 0 || r.Grade == nil {
		t.Fatalf("%+v", r)
	}
}

func TestRunFinishesIdleSessionWithoutReply(t *testing.T) {
	ladder := writeLadder(t)
	opts := options(t, ladder, &script{write: true, wait: true, content: "package probe\n\nfunc Answer() int { return 42 }\n"})
	opts.Idle = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := runAndGrade(t, ctx, opts)
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	r := results[0]
	if !r.NoReply || r.TimedOut || r.Replies != 0 || r.Outcome != eval.Passed || r.Duration > 3500*time.Millisecond {
		t.Fatalf("%+v", r)
	}
	rep, err := eval.Analyze(ctx, opts.Mounts.Results)
	if err != nil || len(rep.Tiers) != 1 || rep.Tiers[0].NoReply != 1 || !strings.Contains(rep.Markdown(), "(no reply)") {
		t.Fatal(rep, err)
	}
}

// A session whose model never answered is an infrastructure error, not a
// failed attempt: three ladder tasks were graded as failures during a server
// outage.
func TestRunClassifiesNeverConnectedSessionAsError(t *testing.T) {
	ladder := writeLadder(t)
	opts := options(t, ladder, &script{fail: errors.New(`vllm: submit: Post "http://model.test/v1/chat/completions": dial tcp: connection refused`)})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := runAndGrade(t, ctx, opts)
	if err == nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	r := results[0]
	if r.Outcome != eval.Errored || r.Grade != nil || !strings.Contains(r.Error, "no model call completed") || !strings.Contains(r.Error, "connection refused") {
		t.Fatalf("%+v", r)
	}
}

func TestSelfCheck(t *testing.T) {
	ladder := writeLadder(t)
	tasks, err := eval.LoadLadder(ladder)
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	checks := eval.SelfCheck(context.Background(), tasks, 2, t.TempDir())
	if len(checks) != 1 || !checks[0].OK() {
		t.Fatalf("%+v", checks)
	}
}

func publicProblems(t *testing.T, ladder string) string {
	t.Helper()
	public := t.TempDir()
	tasks, err := eval.LoadLadder(ladder)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		dest := filepath.Join(public, task.Tier, filepath.Base(task.Dir))
		if err := os.MkdirAll(dest, 0755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(task.Dir, "task.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "task.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(dest, "workspace"), os.DirFS(task.WorkspaceDir())); err != nil {
			t.Fatal(err)
		}
	}
	return public
}

func runAndGrade(t *testing.T, ctx context.Context, opts eval.Options) ([]eval.Result, error) {
	t.Helper()
	results, err := eval.Run(ctx, opts)
	if err != nil || len(results) == 0 || results[0].Outcome != eval.Submitted {
		return results, err
	}
	mounts := opts.Mounts
	mounts.Workspace = t.TempDir()
	result, err := eval.GradeSubmission(ctx, mounts)
	return []eval.Result{result}, err
}
