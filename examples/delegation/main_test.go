package main

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
	"os"
	"strings"
	"testing"
)

func TestDelegationExample(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	old := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = old }()
	main()
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"accepted after failed audit", "Implement: completed", "Test: completed", "Integrate: pending"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
}
func TestScriptRejectsMissingWorkAndToolFailures(t *testing.T) {
	for _, request := range []provider.Request{{Agent: message.ActorID("worker")}, {Messages: []provider.Message{{Role: "tool", Content: content.Text("Tool error: invalid request")}}}} {
		p := &cycleProvider{}
		if _, err := p.Submit(context.Background(), request, nil); err == nil {
			t.Fatal("invalid script state accepted")
		}
	}
}

func TestScriptValidatesAssignmentsAndWorkerCapabilities(t *testing.T) {
	for _, mode := range []string{"missing root work", "stale review notification", "unknown worker work", "delegating worker", "implementing auditor"} {
		t.Run(mode, func(t *testing.T) {
			store := work.New()
			p := &cycleProvider{root: "root", assigned: true, reviews: map[work.SubmissionID]bool{}, session: &workflow.Session{Store: store}}
			w, err := store.AssignWork("root", work.AssignRequest{Assignee: "worker", Task: "task"})
			if err != nil {
				t.Fatal(err)
			}
			request := provider.Request{Agent: "worker", Messages: []provider.Message{{Role: "user", Envelope: &message.Message{Work: &w}}}}
			switch mode {
			case "missing root work":
				request.Agent = "root"
				request.Messages = []provider.Message{{Role: "user", Envelope: &message.Message{Event: &work.Event{Kind: work.ReviewRequested, SubmissionID: "sub", Work: work.Work{ID: "missing"}}}}}
			case "stale review notification":
				request.Agent = "root"
				request.Messages = []provider.Message{{Role: "user", Envelope: &message.Message{Event: &work.Event{Kind: work.ReviewRequested, SubmissionID: "sub", Work: w}}}}
			case "unknown worker work":
				request.Messages[0].Envelope.Work = &work.Work{ID: "missing"}
			case "delegating worker":
				request.Tools = []provider.ToolDefinition{{Name: "assign_work"}}
			case "implementing auditor":
				sub, err := store.SubmitWork("worker", work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "ready"})
				if err != nil {
					t.Fatal(err)
				}
				w, _ = store.GetWork("root", w.ID)
				audit, err := store.AssignAudit("root", work.AssignAuditRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, SubmissionID: sub.ID, Auditor: "reviewer"})
				if err != nil {
					t.Fatal(err)
				}
				request.Agent = "reviewer"
				request.Messages[0].Envelope.Work = &audit
				request.Tools = []provider.ToolDefinition{{Name: "submit_work"}}
			}
			response, err := p.Submit(context.Background(), request, nil)
			if mode == "stale review notification" {
				if err != nil || response.Content != "Waiting for the work cycle." {
					t.Fatal(response, err)
				}
			} else if err == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
}

func TestDelegationCancellationDuringStartupAndExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := 0
	mainWithExit(ctx, func(got int) { code = got })
	if code != 1 {
		t.Fatal(code)
	}
	s := &workflow.Session{Store: work.New()}
	p := &cycleProvider{done: make(chan work.Work, 1), root: "root"}
	if err := reportOutcome(ctx, s, p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p.done <- work.Work{ID: "accepted", State: work.Accepted}
	if err := reportOutcome(context.Background(), s, p); !errors.Is(err, work.ErrNotFound) {
		t.Fatal("missing plan was hidden", err)
	}
}
