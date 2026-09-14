package harness_test

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/roster"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type idle struct{}

func (idle) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: "ready"}, nil
}

type closer struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (c *closer) Close(context.Context) error {
	c.calls.Add(1)
	if c.fail.Swap(false) {
		return errors.New("cleanup failed")
	}
	return nil
}

func TestHeadlessSessionOwnsAssemblyAndWork(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	owned := &closer{}
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: idle{}, Resources: []harness.OwnedResource{{Name: "test", Resource: owned}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	root := s.Root()
	if len(s.Agents()) != 1 || s.Agents()[0].State != agent.Idle {
		t.Fatal(s.Agents())
	}
	cfg.Root.Prompt.Instructions[0] = "caller mutation"
	copy := s.Config()
	copy.Root.Prompt.Instructions[0] = "inspection mutation"
	if s.Config().Root.Prompt.Instructions[0] == "inspection mutation" || s.Config().Root.Prompt.Instructions[0] == "caller mutation" {
		t.Fatal("configuration aliases caller")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w, err := s.AssignWork(ctx, root, work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "test"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.SubmitWork(ctx, w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "done"})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.GetWork(ctx, root, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := s.AssignWork(ctx, root, work.AssignmentRequest{Kind: work.AuditWork, Assignee: createWorker(t, s, roster.Auditor), WorkID: w.ID, ExpectedRevision: w.Revision, SubmissionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitAudit(ctx, audit.Assignee, work.AuditRequest{WorkTarget: work.WorkTarget{ID: audit.ID, ExpectedRevision: audit.Revision}, SubmissionID: sub.ID, Verdict: work.Pass, Summary: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.GetWork(ctx, root, w.ID)
	if err != nil || w.State != work.Accepted {
		t.Fatal(w, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if owned.calls.Load() != 1 {
		t.Fatal("resource closed more than once")
	}
}

type observeProvider struct{ requests chan provider.Request }

func (p *observeProvider) Submit(_ context.Context, r provider.Request, observer provider.Observer) (provider.Response, error) {
	p.requests <- r
	return provider.Response{Content: "ok"}, nil
}
func TestDefaultRoleToolsPreserveCLIOrder(t *testing.T) {
	p := &observeProvider{requests: make(chan provider.Request, 8)}
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, err = s.Send(s.Root(), "inspect")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var request provider.Request
	select {
	case request = <-p.requests:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var names []string
	for _, tool := range request.Tools {
		names = append(names, tool.Name)
	}
	want := []string{"shell", "read_pdf", "read_file", "write_file", "edit_file", "get_audit", "get_plan", "get_work", "get_work_progress", "get_research_brief", "wait_for_input", "create_agent", "update_plan", "assign_work", "cancel_work", "reassign_work", "list_work", "send_message", "message_status", "stop_agent", "pause_agent", "resume_agent", "inspect_agent", "list_agents"}
	if !reflect.DeepEqual(names, want) {
		t.Fatal(names)
	}
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: createWorker(t, s, roster.Implementor), Task: "do work"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case request = <-p.requests:
			if request.Agent == w.Assignee {
				goto worker
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
worker:
	for _, d := range request.Tools {
		if d.Name == "assign_work" || d.Name == "submit_audit" {
			t.Fatalf("worker got %s", d.Name)
		}
	}
	info, err := s.InspectAgent(w.Assignee, conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil || info.Transcript == nil {
		t.Fatal(info, err)
	}
}

func TestStartupFailureReturnsRetryableCleanupOwnership(t *testing.T) {
	owned := &closer{}
	owned.fail.Store(true)
	cfg := harness.DefaultConfig()
	cfg.Model.BaseURL = "invalid"
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Resources: []harness.OwnedResource{{Name: "test", Resource: owned}}})
	if s != nil || err == nil {
		t.Fatal("partial session returned")
	}
	var cleanup *harness.StartupError
	if !errors.As(err, &cleanup) {
		t.Fatal(err)
	}
	if err := cleanup.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if owned.calls.Load() != 2 {
		t.Fatal(owned.calls.Load())
	}
}

func createWorker(t *testing.T, s *harness.Session, role roster.Role) identity.ActorID {
	t.Helper()
	r, e := s.CreateAgent(context.Background(), s.Root(), roster.CreateRequest{Role: role})
	if e != nil {
		t.Fatal(e)
	}
	return r.AgentID
}
