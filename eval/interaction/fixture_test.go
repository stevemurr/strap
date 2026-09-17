package interaction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func TestAuditFixtureUsesRealSupersededSubmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LocalTools = t.TempDir(), nil, false
	cfg.Telemetry.ContextTokens = false
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: &fixtureScript{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	f, err := seedAudit(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if f.Original.State != work.NeedsCheck || f.Original.LatestSubmissionID != f.Submission.ID || f.Original.ActiveRepairID != "" {
		t.Fatalf("fixture must await review of the latest outcome: %+v", f.Original)
	}
	if f.Submission.Supersedes != f.PreviousSubmission || f.PreviousSubmission == "" {
		t.Fatalf("fixture must contain a real replaced submission: %+v", f.Submission)
	}
	if len(f.Plan.Steps) != 1 || f.Plan.Steps[0].Status != work.ReadyForReview {
		t.Fatalf("fixture step should be awaiting review: %+v", f.Plan.Steps)
	}
	previous, err := s.GetSubmission(ctx, f.Root, f.PreviousSubmission)
	if err != nil || previous.WorkID != f.Original.ID {
		t.Fatalf("superseded submission must be readable on same work: %+v, %v", previous, err)
	}
	for _, scenario := range []string{"audit-independent", "audit-wrong-assignee", "audit-old-submission", "audit-stale-revision"} {
		t.Run(scenario, func(t *testing.T) {
			p := f.script(scenario)
			first, err := p.Submit(ctx, provider.Request{}, nil)
			if err != nil || len(first.ToolCalls) != 1 {
				t.Fatalf("first script response: %+v, %v", first, err)
			}
			call := first.ToolCalls[0]
			request, err := tool.DecodeAssignment(call.Name, call.Arguments)
			if err != nil || call.Name != "assign_audit" || call.ID == "" {
				t.Fatalf("script must issue a well-formed assignment: %+v, %v", call, err)
			}
			switch scenario {
			case "audit-wrong-assignee":
				if request.Assignee != f.Implementor {
					t.Fatal("script did not select the implementor")
				}
			case "audit-old-submission":
				if request.SubmissionID != f.PreviousSubmission {
					t.Fatal("script did not select the superseded submission")
				}
			case "audit-stale-revision":
				if request.ExpectedRevision != f.Original.Revision-1 {
					t.Fatal("script did not use the stale revision")
				}
			}
			if scenario != "audit-independent" {
				second, err := p.Submit(ctx, provider.Request{}, nil)
				if err != nil || len(second.ToolCalls) != 1 || second.ToolCalls[0].ID == call.ID {
					t.Fatalf("recovery must be a distinct response and call: %+v, %v", second, err)
				}
				request, err = tool.DecodeAssignment(second.ToolCalls[0].Name, second.ToolCalls[0].Arguments)
				if err != nil {
					t.Fatal(err)
				}
			}
			if request.Kind != work.AuditWork || request.Assignee != f.Auditor || request.WorkID != f.Original.ID || request.ExpectedRevision != f.Original.Revision || request.SubmissionID != f.Submission.ID {
				t.Fatalf("final response must request a current independent audit: %+v", request)
			}
			stopped, stop := context.WithCancel(ctx)
			stop()
			response, err := p.Submit(stopped, provider.Request{}, nil)
			if !errors.Is(err, context.Canceled) || response.Content != "" || len(response.ToolCalls) != 0 {
				t.Fatalf("exhausted script must stop without fabricated output: %+v, %v", response, err)
			}
		})
	}
}
