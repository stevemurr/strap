package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	content string
}

func (p *script) Submit(ctx context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.block {
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	}
	n := p.calls.Add(1)
	if p.write && n == 1 {
		args, _ := json.Marshal(map[string]string{"path": "probe.go", "content": p.content})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "write_file", Arguments: args}}}, nil
	}
	return provider.Response{Content: "Done: Answer returns 42."}, nil
}

func writeLadder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "easy", "00-probe")
	files := map[string]string{
		"task.json":                   `{"id":"easy-00-probe","tier":"easy","title":"Probe","insight":"none","prompt":"Implement Answer in probe.go so it returns 42.","timeout":"2s","test_timeout":"1m"}`,
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
	return eval.Options{Config: cfg, Deps: harness.Dependencies{Provider: p}, Ladder: ladder, Output: filepath.Join(t.TempDir(), "run"), Scratch: t.TempDir(), Log: testWriter{t}, Quiet: 200 * time.Millisecond}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func TestRunPassesAndResumes(t *testing.T) {
	ladder := writeLadder(t)
	p := &script{write: true, content: "package probe\n\nfunc Answer() int { return 42 }\n"}
	opts := options(t, ladder, p)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := eval.Run(ctx, opts)
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
	if _, err := os.Stat(filepath.Join(opts.Output, r.TaskID, "workspace", "probe_hidden_test.go")); err != nil {
		t.Fatal("hidden test not applied:", err)
	}
	// The session ran in the scratch directory, not under the run directory,
	// and nothing was left behind there once the workspace moved.
	trace, _ := os.ReadFile(r.Trace)
	if !strings.Contains(string(trace), opts.Scratch) || strings.Contains(string(trace), filepath.Join(opts.Output, r.TaskID, "workspace")) {
		t.Fatal("session did not run in the scratch directory")
	}
	if left, _ := os.ReadDir(opts.Scratch); len(left) != 0 {
		t.Fatalf("scratch not cleaned: %v", left)
	}
	lines, _ := os.ReadFile(filepath.Join(opts.Output, "results.jsonl"))
	if strings.Count(string(lines), "\n") != 1 {
		t.Fatalf("results.jsonl: %q", lines)
	}
	rep, err := eval.Analyze(ctx, opts.Output)
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
	// A second run with the same output directory reuses the stored result.
	calls := p.calls.Load()
	again, err := eval.Run(ctx, opts)
	if err != nil || len(again) != 1 || again[0].Session != r.Session || p.calls.Load() != calls {
		t.Fatal(again, err, p.calls.Load(), calls)
	}
}

func TestRunGradesFailure(t *testing.T) {
	ladder := writeLadder(t)
	opts := options(t, ladder, &script{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results, err := eval.Run(ctx, opts)
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
	results, err := eval.Run(ctx, opts)
	if err != nil || len(results) != 1 {
		t.Fatal(results, err)
	}
	r := results[0]
	if !r.TimedOut || r.Outcome != eval.Failed || r.Replies != 0 || r.Grade == nil {
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
