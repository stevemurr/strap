package harness_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// submitter submits its assignment on its first call and must never be
// asked for another: a successful submission ends its turn.
type submitter struct{ calls atomic.Int32 }

func (p *submitter) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) > 1 {
		return provider.Response{Content: "a closing reply nobody reads"}, nil
	}
	var assigned work.Work
	for _, m := range r.Messages {
		if m.Envelope != nil && m.Envelope.Work != nil {
			assigned = *m.Envelope.Work
		}
	}
	args, _ := tool.MarshalInput(tool.SubmitInput{WorkTarget: work.WorkTarget{ID: assigned.ID, ExpectedRevision: assigned.Revision}, Summary: "Done."})
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: "submit", Name: "submit_work", Arguments: args}}}, nil
}

// A submission is audited without the manager: the harness creates a new
// auditor and assigns it, and the implementor writes no closing reply.
func TestSubmissionIsAuditedByAFreshAuditorAutomatically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	implementor := &submitter{}
	s, err := harness.New(ctx, testConfig(t, false), harness.Dependencies{Provider: textResponse("ready"), Implementor: harness.AgentDependencies{Provider: implementor}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	for _, d := range s.Configuration().Manager.Tools {
		if d.Name == "assign_audit" {
			t.Fatal("the manager still assigns audits")
		}
	}
	worker := createWorker(t, s, roster.Implementor)
	w, err := s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: worker, Task: "Build it"})
	if err != nil {
		t.Fatal(err)
	}
	var audit work.Work
	for audit.ID == "" {
		page, err := s.ListWork(ctx, s.Manager(), work.ListQuery{Kind: work.AuditWork})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 0 {
			audit, _ = s.GetWork(ctx, s.Manager(), page.Items[0].ID)
		}
		select {
		case <-ctx.Done():
			t.Fatal("no audit was assigned")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if audit.ParentID != w.ID || audit.Assignee == worker || audit.Owner != s.Manager() {
		t.Fatalf("%+v", audit)
	}
	if info, err := s.InspectAgent(audit.Assignee, conversation.InspectOptions{}); err != nil || info.Role != roster.Auditor {
		t.Fatal(info, err)
	}
	original, _ := s.GetWork(ctx, s.Manager(), w.ID)
	if original.State != work.Checking {
		t.Fatalf("original is %s", original.State)
	}
	time.Sleep(100 * time.Millisecond)
	if implementor.calls.Load() != 1 {
		t.Fatalf("the implementor was asked %d times", implementor.calls.Load())
	}
}

// writingSubmitter writes a file, then submits.
type writingSubmitter struct{ calls atomic.Int32 }

func (p *writingSubmitter) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	var assigned work.Work
	for _, m := range r.Messages {
		if m.Envelope != nil && m.Envelope.Work != nil {
			assigned = *m.Envelope.Work
		}
	}
	switch p.calls.Add(1) {
	case 1:
		return operation("write_file", map[string]any{"path": "widget.go", "content": "package widget\n\nfunc Name() string { return \"gizmo\" }\n"})
	case 2:
		args, _ := tool.MarshalInput(tool.SubmitInput{WorkTarget: work.WorkTarget{ID: assigned.ID, ExpectedRevision: assigned.Revision}, Summary: "Renamed to gizmo."})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "submit", Name: "submit_work", Arguments: args}}}, nil
	}
	return provider.Response{Content: "unexpected"}, nil
}

// The auditor is handed the requirements, the submission's summary and the
// changed files' contents with its assignment.
func TestAuditIsHandedTheChangedFilesAndRequirements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	implementor := &writingSubmitter{}
	cfg := testConfig(t, true)
	cfg.AuditBrief = true // Off by default: it did not shorten orientation.
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready"), Implementor: harness.AgentDependencies{Provider: implementor}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	worker := createWorker(t, s, roster.Implementor)
	if _, err = s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: worker, Task: "Rename the widget to gizmo"}); err != nil {
		t.Fatal(err)
	}
	var audit work.Work
	for audit.ID == "" {
		if page, err := s.ListWork(ctx, s.Manager(), work.ListQuery{Kind: work.AuditWork}); err == nil && len(page.Items) > 0 {
			audit, _ = s.GetWork(ctx, s.Manager(), page.Items[0].ID)
		}
		select {
		case <-ctx.Done():
			t.Fatal("no audit was assigned")
		case <-time.After(20 * time.Millisecond):
		}
	}
	for _, want := range []string{"Task: Rename the widget to gizmo", "Submission summary: Renamed to gizmo.", "--- widget.go ---", `return "gizmo"`} {
		if !strings.Contains(audit.Context, want) {
			t.Errorf("audit context lacks %q:\n%s", want, audit.Context)
		}
	}
	instructions := strings.Join(s.Configuration().Auditor.Prompt.Instructions, "\n")
	if !strings.Contains(instructions, "context hands you the requirements") {
		t.Error("the auditor is not told about its brief")
	}
}

// runningSubmitter runs a check twice, a failing one once, then submits.
type runningSubmitter struct{ calls atomic.Int32 }

func (p *runningSubmitter) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	var assigned work.Work
	for _, m := range r.Messages {
		if m.Envelope != nil && m.Envelope.Work != nil {
			assigned = *m.Envelope.Work
		}
	}
	shell := func(cmd string) (provider.Response, error) {
		return operation("shell", map[string]any{"command": cmd, "timeout_ms": nil})
	}
	switch p.calls.Add(1) {
	case 1:
		return shell("exit 3")
	case 2:
		return shell("echo vetted")
	case 3:
		return shell("exit 0")
	case 4:
		return shell("echo vetted")
	case 5:
		args, _ := tool.MarshalInput(tool.SubmitInput{WorkTarget: work.WorkTarget{ID: assigned.ID, ExpectedRevision: assigned.Revision}, Summary: "Checked."})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "submit", Name: "submit_work", Arguments: args}}}, nil
	}
	return provider.Response{Content: "unexpected"}, nil
}

// The auditor is told the implementor's final runs, as recorded: each
// command once, at its latest exit code, so it need not rerun them.
func TestAuditListsTheImplementorsRecordedRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := harness.New(ctx, testConfig(t, true), harness.Dependencies{Provider: textResponse("ready"), Implementor: harness.AgentDependencies{Provider: &runningSubmitter{}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	worker := createWorker(t, s, roster.Implementor)
	if _, err = s.AssignWork(ctx, s.Manager(), work.AssignmentRequest{Kind: work.Implementation, Assignee: worker, Task: "Check it"}); err != nil {
		t.Fatal(err)
	}
	var audit work.Work
	for audit.ID == "" {
		if page, err := s.ListWork(ctx, s.Manager(), work.ListQuery{Kind: work.AuditWork}); err == nil && len(page.Items) > 0 {
			audit, _ = s.GetWork(ctx, s.Manager(), page.Items[0].ID)
		}
		select {
		case <-ctx.Done():
			t.Fatal("no audit was assigned")
		case <-time.After(20 * time.Millisecond):
		}
	}
	for _, want := range []string{"`exit 3` exited 3", "`exit 0` exited 0", "`echo vetted` exited 0", "Do not run these again"} {
		if !strings.Contains(audit.Context, want) {
			t.Errorf("audit context lacks %q:\n%s", want, audit.Context)
		}
	}
	if strings.Count(audit.Context, "echo vetted") != 1 {
		t.Errorf("a repeated command is listed more than once:\n%s", audit.Context)
	}
	if strings.Contains(audit.Context, "Changed files") {
		t.Error("the brief is on by default")
	}
}
