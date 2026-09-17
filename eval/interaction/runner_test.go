package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type testProviderFunc func(context.Context, provider.Request, provider.Observer) (provider.Response, error)

func (f testProviderFunc) Submit(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	return f(ctx, r, o)
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{Config: harness.DefaultConfig(), Output: filepath.Join(t.TempDir(), "run"), Mode: Live, ScenarioIDs: []string{"audit-independent"}, Timeout: 10 * time.Second}
}
func fixtureFromManifest(t *testing.T, dir string) fixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "audit-independent", "001", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Fixture fixture `json:"fixture"`
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.Fixture
}
func responseCall(name string, value any) provider.Response {
	args, _ := json.Marshal(value)
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: "call", Name: name, Arguments: args}}}
}
func trialResult(t *testing.T, opts Options) Result {
	t.Helper()
	report, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 {
		t.Fatal(report)
	}
	return report.Results[0]
}
func assertFailed(t *testing.T, r Result, id string) {
	t.Helper()
	for _, a := range r.Assertions {
		if a.ID == id && !a.Passed {
			return
		}
	}
	t.Fatalf("missing failed assertion %s: %+v", id, r)
}

func TestScriptedConformanceAndSavedPrefixes(t *testing.T) {
	opts := testOptions(t)
	opts.Mode = Scripted
	opts.ScenarioIDs = nil
	opts.Repetitions = 2
	report, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2*len(List()) {
		t.Fatal(len(report.Results))
	}
	for _, r := range report.Results {
		if r.Outcome != "passed" || r.Harness.Total == 0 || r.Harness.Passed != r.Harness.Total || !r.Behavior.OutcomeCorrect {
			t.Fatalf("bad result: %+v", r)
		}
		clean := r.ScenarioID == "audit-independent"
		if r.Behavior.CleanSuccess != clean || r.Behavior.RecoverySuccess == clean {
			t.Fatal(r.Behavior)
		}
		wantCalls := 2
		if clean {
			wantCalls = 1
		}
		if r.ScenarioID == "audit-revision-race" {
			wantCalls = 4
		}
		if r.ModelCalls != wantCalls || r.ToolCalls != wantCalls {
			t.Fatal(r)
		}
		if r.Start.Sequence == 0 || r.Through.Sequence <= r.Start.Sequence {
			t.Fatal("invalid grading boundaries", r)
		}
		reader, err := inspection.OpenJSONL(context.Background(), filepath.Join(opts.Output, r.Trace))
		if err != nil {
			t.Fatal(err)
		}
		head, err := reader.Head(context.Background())
		reader.Close(context.Background())
		if err != nil || head.Cursor.Sequence <= r.Through.Sequence {
			t.Fatal("cleanup must follow grading prefix", head, err)
		}
		for _, a := range r.Assertions {
			if a.Cursor.Sequence <= r.Start.Sequence || a.Cursor.Sequence > r.Through.Sequence {
				t.Fatalf("assertion outside trial: %+v", a)
			}
		}
	}
	saved, err := ReadReport(opts.Output)
	if err != nil || len(saved.Results) != 2*len(List()) {
		t.Fatal(saved, err)
	}
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("reused nonempty directory")
	}
}

func TestLiveProviderStopsBeforeNextDecision(t *testing.T) {
	opts := testOptions(t)
	var calls atomic.Int32
	opts.Provider = testProviderFunc(func(ctx context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
		f := fixtureFromManifest(t, opts.Output)
		switch calls.Add(1) {
		case 1:
			return responseCall("get_work", map[string]any{"work_id": f.Original.ID}), nil
		case 2:
			last := r.Messages[len(r.Messages)-1]
			var got work.Inspection
			if last.Role != "tool" || json.Unmarshal([]byte(last.Content.Text()), &got) != nil || got.Work.ID != f.Original.ID {
				t.Errorf("missing real get_work result: %+v", last)
			}
			return responseCall("assign_audit", tool.AssignAuditArgs{Assignee: f.Auditor, WorkTarget: work.WorkTarget{ID: got.Work.ID, ExpectedRevision: got.Work.Revision}, SubmissionID: got.Work.LatestSubmissionID}), nil
		default:
			t.Error("provider passed grading boundary")
			return provider.Response{Content: "unexpected"}, nil
		}
	})
	r := trialResult(t, opts)
	if r.Outcome != "passed" || !r.Behavior.CleanSuccess || r.ModelCalls != 2 || calls.Load() != 2 {
		t.Fatalf("unexpected live result: %+v", r)
	}
}

func TestPrematureReplyCannotPass(t *testing.T) {
	opts := testOptions(t)
	opts.Provider = testProviderFunc(func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
		return provider.Response{Content: "Finished."}, nil
	})
	r := trialResult(t, opts)
	if r.Outcome != "failed" || !r.Behavior.Scorable || r.StopReason != "reply" || r.Harness.Passed != r.Harness.Total {
		t.Fatal(r)
	}
	assertFailed(t, r, "outcome.original")
	assertFailed(t, r, "outcome.audit_binding")
}

func TestProviderFailureIsNotModelFailure(t *testing.T) {
	opts := testOptions(t)
	opts.Provider = testProviderFunc(func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
		return provider.Response{}, errors.New("model endpoint unavailable")
	})
	r := trialResult(t, opts)
	if r.Outcome != "error" || r.ErrorClass != "provider" || r.Behavior.Scorable || r.ModelCalls != 1 {
		t.Fatal(r)
	}
}

func TestCallAndToolBudgets(t *testing.T) {
	for _, kind := range []string{"call", "tool"} {
		t.Run(kind, func(t *testing.T) {
			opts := testOptions(t)
			if kind == "call" {
				opts.MaxCalls = 1
			} else {
				opts.MaxToolCalls = 1
			}
			opts.Provider = testProviderFunc(func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
				f := fixtureFromManifest(t, opts.Output)
				r := responseCall("get_work", map[string]any{"work_id": f.Original.ID})
				if kind == "tool" {
					r.ToolCalls = append(r.ToolCalls, provider.ToolCall{ID: "second", Name: "list_agents", Arguments: json.RawMessage(`{}`)})
				}
				return r, nil
			})
			r := trialResult(t, opts)
			if r.Outcome != "failed" || r.ErrorClass != "budget" || r.StopReason != kind+"_budget" || r.Behavior.OutcomeCorrect {
				t.Fatal(r)
			}
			if kind == "tool" && r.ToolCalls != 0 {
				t.Fatal("oversized batch partly dispatched", r)
			}
		})
	}
}

func TestTimeoutCancelsBlockedProviderAndRetainsResult(t *testing.T) {
	opts := testOptions(t)
	opts.Timeout = 500 * time.Millisecond
	entered, stopped := make(chan struct{}), make(chan struct{})
	opts.Provider = testProviderFunc(func(ctx context.Context, _ provider.Request, _ provider.Observer) (provider.Response, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return provider.Response{}, ctx.Err()
	})
	r := trialResult(t, opts)
	select {
	case <-entered:
	default:
		t.Fatal("provider never started")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("cleanup did not join provider")
	}
	if r.Outcome != "failed" || r.ErrorClass != "budget" || r.StopReason != "timeout" {
		t.Fatal(r)
	}
	if _, err := os.Stat(filepath.Join(opts.Output, "audit-independent", "001", "result.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsInvalidOptionsBeforeCreatingArtifacts(t *testing.T) {
	for _, bad := range []func(*Options){func(o *Options) { o.Mode = "bad" }, func(o *Options) { o.MaxCalls = -1 }, func(o *Options) { o.ScenarioIDs = []string{"../bad"} }, func(o *Options) { o.Timeout = -time.Second }} {
		opts := testOptions(t)
		bad(&opts)
		if _, err := Run(context.Background(), opts); err == nil {
			t.Fatal("invalid options accepted")
		}
		if _, err := os.Stat(opts.Output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid run created output", err)
		}
	}
}

func TestProviderGateRecognizesReplyBeforeQueuedWake(t *testing.T) {
	f := fixture{Root: "root"}
	facts := []fact{
		{record: eventlog.Record{Session: "session", Sequence: 5, Data: eventlog.Data{Agent: "root", Kind: "message"}}, event: conversation.MessageEvent{Message: message.Message{From: "root", To: message.User, Kind: message.Reply, Content: "Finished."}}},
		{record: eventlog.Record{Session: "session", Sequence: 6, Data: eventlog.Data{Agent: "root", Kind: "output_started"}}},
	}
	b, ok := firstBoundary(facts, eventlog.Cursor{Session: "session", Sequence: 4}, f)
	if !ok || b.cursor.Sequence != 5 || b.reason != "reply" {
		t.Fatal("queued wake escaped earlier reply boundary", b, ok)
	}
}

func TestReasoningLimitRecoveryIsNotCleanSuccess(t *testing.T) {
	opts := testOptions(t)
	opts.Config.ReasoningLimit = 8
	var calls atomic.Int32
	opts.Provider = testProviderFunc(func(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
		if calls.Add(1) == 1 {
			return provider.Response{}, o.OnDelta(provider.Delta{Channel: provider.ChannelReasoning, Text: "too much reasoning"})
		}
		f := fixtureFromManifest(t, opts.Output)
		return f.script("audit-independent").Submit(ctx, r, o)
	})
	r := trialResult(t, opts)
	if r.Outcome != "passed" || !r.Behavior.RecoverySuccess || r.Behavior.CleanSuccess || r.Behavior.OutputErrors != 1 {
		t.Fatal(r)
	}
}
