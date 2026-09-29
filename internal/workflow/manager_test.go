package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func managerSession(t *testing.T) (context.Context, *Session, identity.ActorID) {
	t.Helper()
	ctx, s := recoverySession(t)
	return ctx, s, coord(s)
}

// submitReady reports every scoped step ready and submits the work as actor.
func submitReady(t *testing.T, s *Session, ctx context.Context, actor identity.ActorID, w work.Work) work.SubmitReceipt {
	t.Helper()
	ready := work.ReadyForReview
	var steps []work.StepProgress
	if w.Scope != nil {
		for _, id := range w.Scope.StepIDs {
			steps = append(steps, work.StepProgress{ID: id, Status: &ready})
		}
	}
	progress, err := s.ReportWorkProgress(ctx, actor, work.ReportWorkProgressRequest{WorkID: w.ID, Position: &work.WorkPosition{Objective: w.Task, Note: "Ready.", NextStep: "Submit."}, Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.SubmitWork(ctx, actor, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: progress.WorkRevision}, Summary: "Done."})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

// A session has one manager: no second one can be created, by the manager or
// by the bootstrap again. Only the manager creates agents, and only workers;
// nothing else, the user included, creates any.
func TestManagerCreationFollowsTheHierarchy(t *testing.T) {
	ctx, s, manager := managerSession(t)
	if _, err := s.CreateAgent(ctx, manager, roster.CreateRequest{Role: roster.Manager}); !errors.Is(err, work.ErrInvalid) {
		t.Fatalf("manager created a manager: %v", err)
	}
	if _, err := s.CreateManager(ctx); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("a second manager was created: %v", err)
	}
	if _, err := s.CreateAgent(ctx, message.User, roster.CreateRequest{Role: roster.Implementor}); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("the user created a worker: %v", err)
	}
	implementor, err := s.CreateAgent(ctx, manager, roster.CreateRequest{Role: roster.Implementor})
	if err != nil || implementor.Parent != manager {
		t.Fatal(implementor, err)
	}
	if _, err := s.CreateAgent(ctx, implementor.AgentID, roster.CreateRequest{Role: roster.Auditor}); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("worker created an agent: %v", err)
	}
}

// The manager plans and assigns; it never holds work. Nothing can assign it
// work, including itself, and the workflow gives it no tool to report on or
// submit work.
func TestManagerHoldsNoWork(t *testing.T) {
	ctx, s, manager := managerSession(t)
	if _, err := s.AssignWork(ctx, message.User, work.AssignmentRequest{Kind: work.Implementation, Assignee: manager, Task: "not yours"}); !errors.Is(err, work.ErrForbidden) {
		t.Fatalf("the user assigned work to manager %s: %v", manager, err)
	}
	for _, kind := range []work.Kind{work.Implementation, work.Review} {
		if _, err := s.AssignWork(ctx, manager, work.AssignmentRequest{Kind: kind, Assignee: manager, Task: "do it myself"}); !errors.Is(err, work.ErrInvalid) {
			t.Fatalf("the manager assigned itself %s work: %v", kind, err)
		}
	}
	for _, op := range s.ManagerSpec().Tools {
		switch n := op.Definition().Name; n {
		case "submit_work", "report_work_progress", "reassign_work":
			t.Fatalf("the manager has %s", n)
		}
	}
}

// A worker's reply to its assignment wakes its owner only while that
// assignment is open. After submission the ledger notice carries the outcome.
func TestResolvedAssignmentReplyOnlyAfterTheAssignmentCloses(t *testing.T) {
	ctx, s, manager := managerSession(t)
	implementor, err := s.CreateAgent(ctx, manager, roster.CreateRequest{Role: roster.Implementor})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AssignWork(ctx, manager, work.AssignmentRequest{Kind: work.Implementation, Assignee: implementor.AgentID, Task: "Do it"})
	if err != nil {
		t.Fatal(err)
	}
	var instruction message.MessageID
	for deadline := time.Now().Add(2 * time.Second); instruction == ""; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		for id, b := range s.assignments {
			if b.work == w.ID {
				instruction = id
			}
		}
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("assignment was never delivered")
		}
	}
	reply := message.Message{Kind: message.Reply, From: implementor.AgentID, To: manager, ReplyTo: instruction}
	if s.ResolvedAssignmentReply(reply) {
		t.Fatal("a reply while the assignment is open must wake the owner")
	}
	submitReady(t, s, ctx, implementor.AgentID, w)
	if !s.ResolvedAssignmentReply(reply) {
		t.Fatal("a reply after submission must not wake the owner")
	}
	for _, other := range []message.Message{
		{Kind: message.Reply, From: manager, To: message.User, ReplyTo: instruction},
		{Kind: message.Reply, From: implementor.AgentID, To: manager, ReplyTo: "message-other"},
		{Kind: message.Failure, From: implementor.AgentID, To: manager, ReplyTo: instruction},
	} {
		if s.ResolvedAssignmentReply(other) {
			t.Fatalf("%+v must wake its recipient", other)
		}
	}
}

// An implementor changes the workspace only while it holds work: before its
// assignment and after a cancellation, writes refuse and nothing runs.
func TestImplementorWritesRequireHeldWork(t *testing.T) {
	dir := t.TempDir()
	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	c := conversation.New(context.Background())
	s := New(context.Background(), c, agent.Spec{Provider: idleProvider{}, Tools: files.Tools()}, agent.Spec{Provider: idleProvider{}})
	impl, _ := s.Specs()
	var write tool.Tool
	for _, op := range impl.Tools {
		if op.Definition().Name == "write_file" {
			write = op
		}
	}
	args, _ := tool.MarshalInput(map[string]any{"path": "note.txt", "content": "hi"})
	call := tool.Call{Actor: "worker", Arguments: args}
	written := func() bool { _, err := os.Stat(filepath.Join(dir, "note.txt")); return err == nil }
	if _, err := write.Call(context.Background(), call); err == nil || !strings.Contains(err.Error(), "nothing ran") || written() {
		t.Fatalf("write without work: %v", err)
	}
	w, err := s.Store.AssignWork("owner", work.AssignRequest{Assignee: "worker", Task: "write the note"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := write.Call(context.Background(), call); err != nil || !written() {
		t.Fatalf("write under work: %v", err)
	}
	os.Remove(filepath.Join(dir, "note.txt"))
	if _, err := s.Store.Cancel("owner", work.CancelRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Reason: "requirements changed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := write.Call(context.Background(), call); err == nil || written() {
		t.Fatalf("write after cancellation: %v", err)
	}
}
