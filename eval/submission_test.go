package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eval"
)

func TestOutboxSeparatesAgentFromGrader(t *testing.T) {
	p := &script{write: true, content: "package probe\nfunc Answer() int { return 42 }\n"}
	opts := options(t, writeLadder(t), p)
	private := opts.Mounts.Grading
	opts.Mounts.Grading = "/not-mounted-in-agent-container"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	results, err := eval.Run(ctx, opts)
	if err != nil || len(results) != 1 || results[0].Outcome != eval.Submitted || results[0].Grade != nil {
		t.Fatal(results, err)
	}
	for _, root := range []string{opts.Mounts.Workspace, filepath.Join(opts.Mounts.Outbox, "submission", "workspace")} {
		if _, err := os.Stat(filepath.Join(root, "probe_hidden_test.go")); !os.IsNotExist(err) {
			t.Fatal("hidden test leaked", root, err)
		}
	}
	if _, err := os.Stat(filepath.Join(opts.Mounts.Outbox, ".pending")); !os.IsNotExist(err) {
		t.Fatal("pending submission remains", err)
	}
	// Grading must consume the snapshot even if the original workspace changes.
	if err := os.WriteFile(filepath.Join(opts.Mounts.Workspace, "probe.go"), []byte("broken after submission"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := p.calls.Load()
	mounts := opts.Mounts
	mounts.Problems, mounts.Grading, mounts.Workspace = "/not-mounted-in-grader", private, t.TempDir()
	r, err := eval.GradeSubmission(ctx, mounts)
	if err != nil || !r.Passed || r.Grade == nil {
		t.Fatal(r, err)
	}
	if p.calls.Load() != calls {
		t.Fatal("grading called the model")
	}
	if _, err := os.Stat(filepath.Join(mounts.Workspace, "probe_hidden_test.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mounts.Outbox, "submission", "workspace", "probe_hidden_test.go")); !os.IsNotExist(err) {
		t.Fatal("grader modified submission", err)
	}
	original, _ := os.ReadFile(filepath.Join(opts.Mounts.Workspace, "probe.go"))
	if string(original) != "broken after submission" {
		t.Fatal("grader modified original workspace")
	}
	// Regrading uses a fresh workspace and the same submission and model trace.
	mounts.Workspace = t.TempDir()
	again, err := eval.GradeSubmission(ctx, mounts)
	if err != nil || !again.Passed || again.Session != r.Session || p.calls.Load() != calls {
		t.Fatal(again, err)
	}
}

func TestInterruptedRunDoesNotPublish(t *testing.T) {
	opts := options(t, writeLadder(t), &script{block: true})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := eval.Run(ctx, opts); err == nil {
		t.Fatal("expected cancellation")
	}
	entries, err := os.ReadDir(opts.Mounts.Outbox)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	if _, err := os.Stat(filepath.Join(opts.Mounts.Workspace, "go.mod")); err != nil {
		t.Fatal("workspace lost on interruption", err)
	}
}

func TestGraderRejectsIncompleteSubmission(t *testing.T) {
	opts := options(t, writeLadder(t), &script{})
	if err := os.Mkdir(filepath.Join(opts.Mounts.Outbox, ".pending"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := eval.GradeSubmission(context.Background(), opts.Mounts)
	if err == nil || !strings.Contains(err.Error(), "read ready submission") {
		t.Fatal(err)
	}
}

func TestMountValidationPreservesExistingFiles(t *testing.T) {
	for _, mount := range []string{"workspace", "results", "outbox"} {
		t.Run(mount, func(t *testing.T) {
			p := &script{}
			opts := options(t, writeLadder(t), p)
			dir := map[string]string{"workspace": opts.Mounts.Workspace, "results": opts.Mounts.Results, "outbox": opts.Mounts.Outbox}[mount]
			path := filepath.Join(dir, "keep")
			if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := eval.Run(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), "must be empty") || p.calls.Load() != 0 {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "existing" {
				t.Fatal("overwrote mounted data", err)
			}
		})
	}
}
