package interaction

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// The manifest is written before the first gated request. Reading it here gives
// deliberately faulty providers actual fixture IDs without hard-coded counters.
type adversarialProvider struct {
	dir       string
	build     func(fixture) []provider.Response
	script    *fixtureScript
	firstErr  error
	attempted bool
}

func (p *adversarialProvider) Submit(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	if !p.attempted {
		p.attempted = true
		if p.firstErr != nil {
			return provider.Response{}, p.firstErr
		}
	}
	if p.script == nil {
		body, err := os.ReadFile(filepath.Join(p.dir, "audit-independent", "001", "manifest.json"))
		if err != nil {
			return provider.Response{}, err
		}
		var manifest struct {
			Fixture fixture `json:"fixture"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			return provider.Response{}, err
		}
		p.script = &fixtureScript{responses: p.build(manifest.Fixture)}
	}
	return p.script.Submit(ctx, r, o)
}

func adversarialCall(id, name string, args any) provider.ToolCall {
	b, _ := tool.MarshalInput(args)
	return provider.ToolCall{ID: id, Name: name, Arguments: b}
}

func adversarialAudit(f fixture) provider.ToolCall {
	return adversarialCall("audit", "assign_audit", tool.AssignAuditArgs{Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: f.Original.Revision}, SubmissionID: f.Submission.ID})
}

func runAdversarial(t *testing.T, p *adversarialProvider) Result {
	t.Helper()
	p.dir = t.TempDir()
	report, err := Run(context.Background(), Options{
		Mode: Live, Output: p.dir, ScenarioIDs: []string{"audit-independent"},
		Provider: p, Timeout: 10 * time.Second,
	})
	if err != nil || len(report.Results) != 1 {
		t.Fatalf("run: %+v, %v", report, err)
	}
	result := report.Results[0]
	if result.ErrorClass != "" || !result.Behavior.Scorable || result.Harness.Passed != result.Harness.Total {
		t.Fatalf("actor mistakes must not become harness or provider failures: %+v", result)
	}
	return result
}

func TestAdversarialAssignThenCancelSameBatch(t *testing.T) {
	p := &adversarialProvider{build: func(f fixture) []provider.Response {
		return []provider.Response{{ToolCalls: []provider.ToolCall{
			adversarialAudit(f),
			adversarialCall("cancel", "cancel_work", work.CancelRequest{
				WorkTarget: work.WorkTarget{ID: f.Original.ID, ExpectedRevision: f.Original.Revision + 1},
				Reason:     "Deliberately undo the requested result within the same response.",
			}),
		}}}
	}}
	result := runAdversarial(t, p)
	if result.Outcome != "failed" || result.Behavior.OutcomeCorrect || result.ToolCalls != 2 {
		t.Fatalf("later cancellation in assignment batch escaped grading: %+v", result)
	}
	failedOutcome, failedEffects := false, false
	for _, a := range result.Assertions {
		failedOutcome = failedOutcome || a.ID == "outcome.original" && !a.Passed
		failedEffects = failedEffects || a.ID == "effects.no_extra_actions" && !a.Passed
	}
	if !failedOutcome || !failedEffects {
		t.Fatal("same-batch cancellation must fail both final state and extra-action assertions")
	}
}

func TestAdversarialStoppedAuditorIsModelFailure(t *testing.T) {
	p := &adversarialProvider{build: func(f fixture) []provider.Response {
		return []provider.Response{
			{ToolCalls: []provider.ToolCall{
				adversarialCall("stop", "stop_agent", map[string]any{"agent_id": f.Auditor}),
				adversarialAudit(f),
			}},
			{Content: "I could not assign the stopped auditor."},
		}
	}}
	result := runAdversarial(t, p)
	if result.Outcome != "failed" || result.Behavior.UnknownToolErrors != 1 || result.ToolCalls != 2 {
		t.Fatalf("stopped auditor should be an actor error with valid runtime rejection: %+v", result)
	}
}

func TestAdversarialUnknownToolThenCorrection(t *testing.T) {
	p := &adversarialProvider{build: func(f fixture) []provider.Response {
		return []provider.Response{
			{ToolCalls: []provider.ToolCall{adversarialCall("unknown", "invented_audit_tool", map[string]any{})}},
			{ToolCalls: []provider.ToolCall{adversarialAudit(f)}},
		}
	}}
	result := runAdversarial(t, p)
	if result.Outcome != "passed" || !result.Behavior.RecoverySuccess || result.Behavior.UnknownToolErrors != 1 || result.Behavior.CleanSuccess || result.ModelCalls != 2 {
		t.Fatalf("unknown tool must remain visible after correction: %+v", result)
	}
}

// A real pilot run mixed repair fields into an audit assignment on the previous
// tool surface. Keep that failure mode covered on the current operation-specific tool. Rejection is a model mistake, not a side effect.
func TestAdversarialWrongAssignmentBranchThenCorrection(t *testing.T) {
	p := &adversarialProvider{build: func(f fixture) []provider.Response {
		return []provider.Response{
			{ToolCalls: []provider.ToolCall{adversarialCall("wrong-branch", "assign_audit", map[string]any{"assignee": f.Auditor, "work_id": f.Original.ID, "expected_revision": f.Original.Revision, "audit_id": f.Original.LatestAuditID})}},
			{ToolCalls: []provider.ToolCall{adversarialAudit(f)}},
		}
	}}
	r := runAdversarial(t, p)
	if r.Outcome != "passed" || !r.Behavior.RecoverySuccess || r.Behavior.CleanSuccess || r.Behavior.RejectedCalls != 1 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("schema correction must remain a recovered success: %+v", r)
	}
}

func TestAdversarialMalformedOutputThenCorrection(t *testing.T) {
	p := &adversarialProvider{
		firstErr: &provider.ToolArgumentsError{Name: "assign_audit", CallID: "truncated", Arguments: `{"input":{"assignee":}`, FinishReason: "length"},
		build: func(f fixture) []provider.Response {
			return []provider.Response{{ToolCalls: []provider.ToolCall{adversarialAudit(f)}}}
		},
	}
	result := runAdversarial(t, p)
	if result.Outcome != "passed" || result.Behavior.OutputErrors != 1 || !result.Behavior.RecoverySuccess || result.Behavior.CleanSuccess || result.ToolCalls != 1 || result.ModelCalls != 2 {
		t.Fatalf("discarded malformed output must count as recovered model error: %+v", result)
	}
}
