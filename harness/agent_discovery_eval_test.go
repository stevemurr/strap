package harness_test

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
	"os"
	"testing"
	"time"
)

// Opt-in discovery evaluation: the root uses a real model, while idle delegates
// isolate whether natural language selects the correct public operations.
// STRAP_EVAL_URL and STRAP_EVAL_MODEL must both be set. Full real execution is
// additionally exercised by examples/local; this test grades actual tool calls.
// Rejected calls are counted separately from outcome correctness; at most two
// corrections are allowed. STRAP_EVAL_STRICT=1 requires zero rejected calls.
func TestAgentDiscoveryLive(t *testing.T) {
	url, model := os.Getenv("STRAP_EVAL_URL"), os.Getenv("STRAP_EVAL_MODEL")
	if url == "" || model == "" {
		t.Skip("set STRAP_EVAL_URL and STRAP_EVAL_MODEL for live discovery eval")
	}
	for _, scenario := range []string{"create", "reuse", "audit", "repair", "replace", "worker_help"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			cfg := harness.DefaultConfig()
			cfg.Dir = t.TempDir()
			cfg.Web = nil
			cfg.LocalTools = false
			cfg.Telemetry.ContextTokens = false
			cfg.Model = harness.ModelConfig{Backend: "chatcompletions", BaseURL: url, Model: model, Timeout: 3 * time.Minute}
			deps := harness.Dependencies{Implementor: harness.AgentDependencies{Provider: idle{}}, Auditor: harness.AgentDependencies{Provider: idle{}}}
			if scenario == "worker_help" {
				deps = harness.Dependencies{Root: harness.AgentDependencies{Provider: idle{}}, Auditor: harness.AgentDependencies{Provider: idle{}}}
			}
			s, e := harness.New(ctx, cfg, deps)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Dispose(context.Background())
			if _, e = s.PauseAgent(s.Root()); e != nil {
				t.Fatal(e)
			}
			for {
				in, e := s.InspectAgent(s.Root(), conversation.InspectOptions{})
				if e != nil {
					t.Fatal(e)
				}
				if in.State == agent.Paused {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
			var selected identity.ActorID
			var original work.Work
			if scenario != "create" {
				selected = createWorker(t, s, roster.Implementor)
			}
			if scenario == "audit" || scenario == "repair" || scenario == "replace" {
				original, e = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: selected, Task: "Calculate 2+2", Context: "This is a fixture for workflow discovery."})
				if e != nil {
					t.Fatal(e)
				}
				if scenario != "replace" {
					sub, e := s.SubmitWork(ctx, selected, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: original.ID, ExpectedRevision: original.Revision}, Summary: "4"})
					if e != nil {
						t.Fatal(e)
					}
					original, _ = s.GetWork(ctx, s.Root(), original.ID)
					if scenario == "repair" {
						auditor := createWorker(t, s, roster.Auditor)
						audit, e := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.AuditWork, Assignee: auditor, WorkID: original.ID, ExpectedRevision: original.Revision, SubmissionID: sub.ID})
						if e != nil {
							t.Fatal(e)
						}
						if _, e = s.SubmitAudit(ctx, auditor, work.AuditRequest{WorkTarget: work.WorkTarget{ID: audit.ID, ExpectedRevision: audit.Revision}, SubmissionID: sub.ID, Verdict: work.Fail, Summary: "Explanation missing", Findings: []work.Finding{{Description: "No explanation", RequiredChange: "Explain addition", Verification: "Read the explanation"}}}); e != nil {
							t.Fatal(e)
						}
					}
				} else {
					if _, e = s.StopAgent(selected); e != nil {
						t.Fatal(e)
					}
				}
			}
			// Capture the accepted boundary after fixture setup.
			reader, e := s.Trace(ctx)
			if e != nil {
				t.Fatal(e)
			}
			h, e := reader.Head(ctx)
			reader.Close(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			prompts := map[string]string{
				"create":  "Create an agent and have it calculate 2+2, 3+3, and 4+4. Delegate those three calculations as one task.",
				"reuse":   "Reuse agent " + string(selected) + " to calculate 5+5. Give it a new task.",
				"audit":   "Have another agent independently audit the submitted answer for work " + string(original.ID) + ".",
				"repair":  "Repair the failed audit for work " + string(original.ID) + ". Start the repair now.",
				"replace": "Replace the stopped worker " + string(selected) + " and transfer work " + string(original.ID) + " to the replacement.",
			}
			if scenario == "worker_help" {
				_, e = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: selected, Task: "You need another agent to independently calculate 8+8 before you can proceed. Ask your owner to provide that help and report a blocker. Do not calculate or submit the answer yourself."})
			} else {
				_, e = s.Send(s.Root(), prompts[scenario])
				if e == nil {
					_, e = s.ResumeAgent(s.Root())
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			sub, e := s.Subscribe(ctx, harness.SubscribeOptions{After: h.Cursor})
			if e != nil {
				t.Fatal(e)
			}
			defer sub.Close()
			created := false
			validationErrors := 0
			defer func() { t.Logf("discovery rejected calls: %d", validationErrors) }()
			for {
				record, e := sub.Next(ctx)
				if e != nil {
					t.Fatal("discovery did not reach expected operation", e)
				}
				if record.Kind != "tool" {
					continue
				}
				resolved, e := s.ResolveRecord(ctx, record)
				if e != nil {
					t.Fatal(e)
				}
				fact, e := eventcodec.DecodeEvent(resolved)
				if e != nil {
					t.Fatal(e)
				}
				activity := fact.(conversation.ToolEvent).Activity
				if activity.FinishedAt.IsZero() {
					continue
				}
				t.Logf("%s %s %s error=%v", record.Agent, activity.Call.Name, activity.Call.Arguments, activity.Err)
				if activity.Err != nil {
					validationErrors++
					if os.Getenv("STRAP_EVAL_STRICT") == "1" || validationErrors > 2 {
						t.Fatal("model issued invalid operation", activity.Err)
					}
					t.Logf("first-attempt error %d; allowing the model to correct the rejected call", validationErrors)
					continue
				}
				if scenario == "worker_help" {
					if record.Agent == string(selected) && activity.Call.Name == "send_message" {
						var args struct {
							To identity.ActorID `json:"to"`
						}
						json.Unmarshal(activity.Call.Arguments, &args)
						if args.To != s.Root() {
							t.Fatal("help addressed to wrong actor")
						}
						return
					}
					continue
				}
				if record.Agent != string(s.Root()) {
					continue
				}
				if activity.Call.Name == "create_agent" {
					var r roster.CreateRequest
					json.Unmarshal(activity.Call.Arguments, &r)
					want := roster.Implementor
					if scenario == "audit" {
						want = roster.Auditor
					}
					if r.Role != want {
						t.Fatal("wrong role", r)
					}
					if scenario == "reuse" {
						t.Fatal("created instead of reusing")
					}
					created = true
				}
				if activity.Call.Name == "assign_implementation" || activity.Call.Name == "assign_audit" || activity.Call.Name == "assign_repair" || activity.Call.Name == "assign_research" {
					r, e := tool.DecodeAssignment(activity.Call.Name, activity.Call.Arguments)
					if e != nil {
						t.Fatal(e)
					}
					want := work.Implementation
					if scenario == "audit" {
						want = work.AuditWork
					}
					if scenario == "repair" {
						want = work.Repair
					}
					if r.Kind != want || r.Assignee == "" {
						t.Fatal("wrong assignment", r)
					}
					if scenario == "create" || scenario == "audit" {
						if !created {
							t.Fatal("missing explicit creation")
						}
					}
					if scenario == "reuse" && r.Assignee != selected {
						t.Fatal("did not reuse selected agent")
					}
					if scenario == "replace" {
						t.Fatal("assigned new work instead of transferring existing work")
					}
					return
				}
				if activity.Call.Name == "reassign_work" && scenario == "replace" {
					var r work.ReassignRequest
					json.Unmarshal(activity.Call.Arguments, &r)
					if !created || r.ID != original.ID || r.Assignee == selected || r.Assignee == "" {
						t.Fatal("invalid replacement", r)
					}
					return
				}
			}
		})
	}
}
