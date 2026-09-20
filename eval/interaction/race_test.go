package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

const raceScenario = "audit-revision-race"

func raceOptions(t *testing.T) Options {
	t.Helper()
	opts := testOptions(t)
	opts.ScenarioIDs = []string{raceScenario}
	return opts
}

func raceFixture(t *testing.T, dir string) fixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, raceScenario, "001", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Fixture fixture `json:"fixture"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.Fixture
}

func raceReceipt(request provider.Request, callID string) (string, error) {
	for i := len(request.Messages) - 1; i >= 0; i-- {
		m := request.Messages[i]
		if m.Role == "tool" && m.ToolCallID == callID {
			return m.Content.Text(), nil
		}
	}
	return "", fmt.Errorf("missing production tool receipt for %q", callID)
}

func raceInspection(request provider.Request, callID string) (work.Inspection, error) {
	var got work.Inspection
	text, err := raceReceipt(request, callID)
	if err != nil {
		return got, err
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		return got, fmt.Errorf("invalid get_work receipt: %w", err)
	}
	if got.Work.ID == "" || got.Work.Revision == 0 {
		return got, fmt.Errorf("incomplete get_work receipt: %s", text)
	}
	return got, nil
}

func TestRevisionRaceScriptedConformanceAndSavedMetadata(t *testing.T) {
	opts := raceOptions(t)
	opts.Mode = Scripted
	r := trialResult(t, opts)
	if r.Outcome != "passed" || !r.Behavior.RecoverySuccess || r.Behavior.CleanSuccess || r.Behavior.RejectedCalls != 1 || r.ModelCalls != 4 || r.ToolCalls != 4 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("scripted read/conflict/refresh/retry must recover: %+v", r)
	}
	f := raceFixture(t, opts.Output)
	assertRaceMetadata(t, r, f)
	body, err := os.ReadFile(filepath.Join(opts.Output, raceScenario, "001", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Result
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.RevisionRace, r.RevisionRace) {
		t.Fatal("intervention evidence was not preserved in result.json")
	}
	report, err := ReadReport(opts.Output)
	if err != nil || len(report.Results) != 1 || !reflect.DeepEqual(report.Results[0].RevisionRace, r.RevisionRace) {
		t.Fatalf("intervention evidence was not preserved in replayable report: %+v, %v", report, err)
	}
}

func TestRevisionRaceLiveProviderUsesActualConflictAndFreshRead(t *testing.T) {
	opts := raceOptions(t)
	var calls atomic.Int32
	var before, after work.Work
	var proposed provider.ToolCall
	opts.Provider = testProviderFunc(func(_ context.Context, request provider.Request, _ provider.Observer) (provider.Response, error) {
		f := raceFixture(t, opts.Output)
		switch calls.Add(1) {
		case 1:
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall("read-before", "get_work", map[string]any{"work_id": f.Original.ID})}}, nil
		case 2:
			got, err := raceInspection(request, "read-before")
			if err != nil {
				return provider.Response{}, err
			}
			before = got.Work
			assignment := tool.AssignAuditArgs{Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: before.ID, ExpectedRevision: before.Revision}, SubmissionID: before.LatestSubmissionID}
			// Preserve deliberate whitespace too: the race changes domain state,
			// never the provider's submitted arguments.
			args, err := json.MarshalIndent(tool.Input[tool.AssignAuditArgs]{Value: assignment}, "", "  ")
			if err != nil {
				return provider.Response{}, err
			}
			proposed = provider.ToolCall{ID: "assign-before", Name: "assign_audit", Arguments: args}
			return provider.Response{ToolCalls: provider.CopyCalls([]provider.ToolCall{proposed})}, nil
		case 3:
			text, err := raceReceipt(request, "assign-before")
			if err != nil {
				return provider.Response{}, err
			}
			if !strings.Contains(text, work.ErrConflict.Error()) || !strings.Contains(text, fmt.Sprintf("is at revision %d", before.Revision+2)) || !strings.Contains(text, fmt.Sprintf("expected_revision was %d", before.Revision)) {
				return provider.Response{}, fmt.Errorf("missing authentic revision conflict: %s", text)
			}
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall("read-after", "get_work", map[string]any{"work_id": before.ID})}}, nil
		case 4:
			got, err := raceInspection(request, "read-after")
			if err != nil {
				return provider.Response{}, err
			}
			after = got.Work
			if after.ID != before.ID || after.State != work.NeedsCheck || after.Revision != before.Revision+2 || after.LatestSubmissionID != before.LatestSubmissionID {
				return provider.Response{}, fmt.Errorf("fresh read has incorrect race state: %+v", after)
			}
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall("assign-after", "assign_audit", tool.AssignAuditArgs{Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: after.ID, ExpectedRevision: after.Revision}, SubmissionID: after.LatestSubmissionID})}}, nil
		default:
			return provider.Response{}, errors.New("provider passed successful assignment boundary")
		}
	})
	r := trialResult(t, opts)
	if r.Outcome != "passed" || !r.Behavior.RecoverySuccess || r.Behavior.CleanSuccess || r.Behavior.RejectedCalls != 1 || r.ModelCalls != 4 || r.ToolCalls != 4 || calls.Load() != 4 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("live recovery did not use production receipts: %+v", r)
	}
	f := raceFixture(t, opts.Output)
	assertRaceMetadata(t, r, f)
	if !reflect.DeepEqual(r.RevisionRace.OriginalBefore, before) || !reflect.DeepEqual(r.RevisionRace.OriginalAfter, after) {
		t.Fatal("model's real reads differ from recorded intervention states")
	}
	reader, err := inspection.OpenJSONL(context.Background(), filepath.Join(opts.Output, r.Trace))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	facts, err := readFacts(context.Background(), reader, r.Through)
	if err != nil {
		t.Fatal(err)
	}
	starts, finishes := 0, 0
	for _, fact := range facts {
		e, ok := fact.event.(conversation.ToolEvent)
		if !ok || e.Agent != f.Root || e.Activity.Call.ID != proposed.ID {
			continue
		}
		if !reflect.DeepEqual(e.Activity.Call, proposed) {
			t.Fatalf("intervention rewrote model call: got %+v, want %+v", e.Activity.Call, proposed)
		}
		if fact.record.Sequence <= r.RevisionRace.Through.Sequence {
			t.Fatal("trigger tool began before host intervention had completed")
		}
		if e.Activity.FinishedAt.IsZero() {
			starts++
		} else {
			finishes++
			if e.Activity.Err == nil || !strings.Contains(e.Activity.Err.Error(), work.ErrConflict.Error()) {
				t.Fatalf("trigger did not receive real ledger conflict: %+v", e.Activity)
			}
		}
	}
	if starts != 1 || finishes != 1 {
		t.Fatalf("expected one unmodified trigger invocation, got starts=%d finishes=%d", starts, finishes)
	}
}

func TestRevisionRaceStaleRetryLoopIsBoundedBehaviorFailure(t *testing.T) {
	opts := raceOptions(t)
	opts.MaxCalls = 4
	var calls atomic.Int32
	var stale work.Work
	opts.Provider = testProviderFunc(func(_ context.Context, request provider.Request, _ provider.Observer) (provider.Response, error) {
		f := raceFixture(t, opts.Output)
		n := calls.Add(1)
		if n == 1 {
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall("read-before", "get_work", map[string]any{"work_id": f.Original.ID})}}, nil
		}
		if n == 2 {
			got, err := raceInspection(request, "read-before")
			if err != nil {
				return provider.Response{}, err
			}
			stale = got.Work
		}
		return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall(fmt.Sprintf("stale-%d", n), "assign_audit", tool.AssignAuditArgs{Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: stale.ID, ExpectedRevision: stale.Revision}, SubmissionID: stale.LatestSubmissionID})}}, nil
	})
	r := trialResult(t, opts)
	if r.Outcome != "failed" || r.ErrorClass != "budget" || r.StopReason != "call_budget" || !r.Behavior.Scorable || r.Behavior.OutcomeCorrect || r.Behavior.RecoverySuccess || r.Behavior.RejectedCalls != 3 || r.ModelCalls != 4 || r.ToolCalls != 4 || calls.Load() != 4 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("stale retries should exhaust actor budget with healthy harness: %+v", r)
	}
	assertRaceMetadata(t, r, raceFixture(t, opts.Output))
	assertFailed(t, r, "race.recovered_with_current_revision")
}

func TestRevisionRacePrematureReplyBeforeInjectionIsScorable(t *testing.T) {
	opts := raceOptions(t)
	opts.Provider = testProviderFunc(func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
		return provider.Response{Content: "Finished."}, nil
	})
	r := trialResult(t, opts)
	if r.Outcome != "failed" || r.ErrorClass != "" || !r.Behavior.Scorable || r.StopReason != "reply" || r.RevisionRace != nil || r.ModelCalls != 1 || r.ToolCalls != 0 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("no assignment must remain model failure without invented interference: %+v", r)
	}
	assertFailed(t, r, "race.revision_rejected")
	assertFailed(t, r, "race.recovered_with_current_revision")
}

func TestRevisionRaceDiscardedReasoningLimitResponseCannotInject(t *testing.T) {
	opts := raceOptions(t)
	opts.Config.ReasoningLimit = 8
	var calls atomic.Int32
	opts.Provider = testProviderFunc(func(ctx context.Context, request provider.Request, observer provider.Observer) (provider.Response, error) {
		f := raceFixture(t, opts.Output)
		assignment := func(id string, w work.Work) provider.Response {
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall(id, "assign_audit", tool.AssignAuditArgs{Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, SubmissionID: w.LatestSubmissionID})}}
		}
		switch calls.Add(1) {
		case 1:
			err := observer.OnDelta(provider.Delta{Channel: provider.ChannelReasoning, Text: "reasoning over the eight-byte limit"})
			if !errors.Is(err, agent.ErrReasoningLimit) || ctx.Err() == nil {
				return provider.Response{}, fmt.Errorf("expected reasoning-limit cancellation, got error=%v context=%v", err, ctx.Err())
			}
			// Deliberately ignore the observer error, as a provider may do. The
			// cancelled response must not reach the host intervention or ledger.
			return assignment("discarded-assignment", f.Original), nil
		case 2:
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall("read-before", "get_work", map[string]any{"work_id": f.Original.ID})}}, nil
		case 3:
			got, err := raceInspection(request, "read-before")
			if err != nil {
				return provider.Response{}, err
			}
			if !reflect.DeepEqual(got.Work, f.Original) {
				return provider.Response{}, fmt.Errorf("discarded response changed work state: %+v", got.Work)
			}
			return assignment("valid-trigger", got.Work), nil
		case 4:
			text, err := raceReceipt(request, "valid-trigger")
			if err != nil {
				return provider.Response{}, err
			}
			if !strings.Contains(text, work.ErrConflict.Error()) {
				return provider.Response{}, fmt.Errorf("valid response did not trigger actual race: %s", text)
			}
			return provider.Response{ToolCalls: []provider.ToolCall{adversarialCall("read-after", "get_work", map[string]any{"work_id": f.Original.ID})}}, nil
		case 5:
			got, err := raceInspection(request, "read-after")
			if err != nil {
				return provider.Response{}, err
			}
			return assignment("recovered-assignment", got.Work), nil
		default:
			return provider.Response{}, errors.New("provider passed successful assignment boundary")
		}
	})
	r := trialResult(t, opts)
	if r.Outcome != "passed" || r.ErrorClass != "" || !r.Behavior.RecoverySuccess || r.Behavior.CleanSuccess || r.Behavior.OutputErrors != 1 || r.Behavior.RejectedCalls != 1 || r.ModelCalls != 5 || r.ToolCalls != 4 || calls.Load() != 5 || r.Harness.Passed != r.Harness.Total {
		t.Fatalf("discarded reasoning output caused interference or hid recovery: %+v", r)
	}
	assertRaceMetadata(t, r, raceFixture(t, opts.Output))
	if r.RevisionRace.TriggerCallID != "valid-trigger" {
		t.Fatalf("discarded response triggered intervention: %+v", r.RevisionRace)
	}
	reader, err := inspection.OpenJSONL(context.Background(), filepath.Join(opts.Output, r.Trace))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	facts, err := readFacts(context.Background(), reader, r.Through)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range facts {
		if e, ok := fact.event.(conversation.ToolEvent); ok && e.Activity.Call.ID == "discarded-assignment" {
			t.Fatal("discarded provider output reached production tool execution")
		}
	}
}

func assertRaceMetadata(t *testing.T, result Result, f fixture) {
	t.Helper()
	r := result.RevisionRace
	if r == nil {
		t.Fatal("missing recorded intervention")
	}
	if r.Before.Session != result.Start.Session || r.Through.Session != result.Start.Session || r.Before.Sequence < result.Start.Sequence || r.Through.Sequence <= r.Before.Sequence || r.Through.Sequence >= result.Through.Sequence {
		t.Fatalf("intervention outside scored trace prefix: %+v", r)
	}
	wantAfter := f.Original.Clone()
	wantAfter.Revision += 2
	if !reflect.DeepEqual(r.OriginalBefore, f.Original) || !reflect.DeepEqual(r.OriginalAfter, wantAfter) {
		t.Fatalf("competing audit should only advance original revision twice: %+v", r)
	}
	if r.CancelledAudit.Kind != work.AuditWork || r.CancelledAudit.State != work.Cancelled || r.CancelledAudit.Revision != 2 || r.CancelledAudit.ParentID != f.Original.ID || r.CancelledAudit.SubjectSubmissionID != f.Submission.ID || r.CancelledAudit.Assignee != f.Auditor {
		t.Fatalf("intervention must preserve identifiable cancelled competing audit: %+v", r.CancelledAudit)
	}
	if r.TriggerCallID == "" || r.TriggerRequest.WorkID != f.Original.ID || r.TriggerRequest.ExpectedRevision != f.Original.Revision || r.TriggerRequest.SubmissionID != f.Submission.ID || r.TriggerRequest.Assignee != f.Auditor {
		t.Fatalf("intervention trigger lacks original valid assignment: %+v", r)
	}
}
