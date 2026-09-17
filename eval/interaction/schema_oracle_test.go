package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func schemaOracleFixture(t *testing.T, id string) (Scenario, fixture, Result, []fact) {
	t.Helper()
	dir := t.TempDir()
	report, err := Run(context.Background(), Options{Mode: Scripted, Output: dir, ScenarioIDs: []string{id}, Timeout: 10 * time.Second})
	if err != nil || len(report.Results) != 1 {
		t.Fatalf("fixture run: %+v %v", report, err)
	}
	result := report.Results[0]
	if result.Outcome != "passed" {
		t.Fatalf("passing schema fixture required: %+v", result)
	}
	if result.Schema == nil || result.Schema.Attempts != 2 || result.Schema.InvalidCalls != 1 || result.Schema.FirstArgumentsValid || !result.Behavior.RecoverySuccess || result.Behavior.CleanSuccess {
		t.Fatalf("scripted probes must retain first-attempt failure after correction: %+v", result)
	}
	wantToolCorrect := id != "schema-removed-assignment-tool"
	if result.Schema.FirstToolCorrect != wantToolCorrect {
		t.Fatalf("wrong tool selection score: %+v", result.Schema)
	}
	body, err := os.ReadFile(filepath.Join(dir, result.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Scenario Scenario `json:"scenario"`
		Fixture  fixture  `json:"fixture"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	reader, err := inspection.OpenJSONL(context.Background(), filepath.Join(dir, result.Trace))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	facts, err := readFacts(context.Background(), reader, result.Through)
	if err != nil {
		t.Fatal(err)
	}
	return manifest.Scenario, manifest.Fixture, result, facts
}

func TestSchemaOracleRequiresRealRejectionAndProbe(t *testing.T) {
	scenario, f, baseline, facts := schemaOracleFixture(t, "schema-audit-repair-field")
	for _, tc := range []struct {
		name, assertion string
		mutate          func([]fact) []fact
	}{
		{"missing probe", "scenario.schema_probe_exercised", func(facts []fact) []fact {
			out := facts[:0]
			for _, x := range facts {
				if e, ok := x.event.(conversation.ToolEvent); ok && x.record.Sequence > baseline.Start.Sequence {
					probe, _ := schemaProbe(scenario.ID, e.Activity.Call.Name, e.Activity.Call.Arguments)
					if probe {
						continue
					}
				}
				out = append(out, x)
			}
			return out
		}},
		{"wrong error category", "schema.probe_error", func(facts []fact) []fact {
			for i, x := range facts {
				if e, ok := x.event.(conversation.ToolEvent); ok && x.record.Sequence > baseline.Start.Sequence && e.Activity.Err != nil {
					e.Activity.Err = errors.New("work: stale revision")
					facts[i].event = e
				}
			}
			return facts
		}},
		{"accepted invalid arguments", "schema.rejected", func(facts []fact) []fact {
			for i, x := range facts {
				if e, ok := x.event.(conversation.ToolEvent); ok && x.record.Sequence > baseline.Start.Sequence && e.Activity.Err != nil {
					e.Activity.Err = nil
					facts[i].event = e
				}
			}
			return facts
		}},
		{"wrong success receipt", "schema.receipt", func(facts []fact) []fact {
			for i, x := range facts {
				if e, ok := x.event.(conversation.ToolEvent); ok && x.record.Sequence > baseline.Start.Sequence && !e.Activity.FinishedAt.IsZero() && e.Activity.Err == nil {
					e.Activity.Result = tool.Text(`{"work_id":"wrong-receipt"}`)
					facts[i].event = e
				}
			}
			return facts
		}},
		{"collateral orphan report", "schema.transition", func(facts []fact) []fact {
			for i, x := range facts {
				if e, ok := x.event.(conversation.WorkEvent); ok && x.record.Sequence > baseline.Start.Sequence && e.Event.Change != nil {
					e.Event.Change.ProgressReports = append(e.Event.Change.ProgressReports, work.WorkProgressReport{ID: "orphan", WorkID: f.Original.ID})
					facts[i].event = e
				}
			}
			return facts
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := oracleResult(baseline)
			gradeSchema(&result, scenario, f, tc.mutate(cloneOracleFacts(facts)))
			requireOracleHarnessFailure(t, result, tc.assertion)
		})
	}

	t.Run("rejection cannot create records even when work is unchanged", func(t *testing.T) {
		mutated := cloneOracleFacts(facts)
		for i, x := range mutated {
			if e, ok := x.event.(conversation.ToolEvent); ok && x.record.Sequence > baseline.Start.Sequence && e.Activity.Err != nil {
				injected := fact{record: x.record, event: conversation.WorkEvent{Event: work.Event{Change: &work.Change{ProgressReports: []work.WorkProgressReport{{ID: "orphan", WorkID: f.Original.ID}}}}}}
				injected.record.Kind = "work"
				for j := i; j < len(mutated); j++ {
					mutated[j].record.Sequence++
				}
				mutated = append(mutated[:i], append([]fact{injected}, mutated[i:]...)...)
				break
			}
		}
		result := oracleResult(baseline)
		result.Through.Sequence++
		gradeSchema(&result, scenario, f, mutated)
		requireOracleHarnessFailure(t, result, "schema.rejection_atomic")
	})

	t.Run("schema regression is caught despite runtime rejection", func(t *testing.T) {
		broken := f
		copySchema := *f.Schema
		broken.Schema = &copySchema
		broken.Schema.Tools = append([]provider.ToolDefinition(nil), f.Schema.Tools...)
		for i := range broken.Schema.Tools {
			if broken.Schema.Tools[i].Name == "assign_audit" {
				broken.Schema.Tools[i].Parameters = json.RawMessage(`{"type":"object"}`)
			}
		}
		result := oracleResult(baseline)
		gradeSchema(&result, scenario, broken, facts)
		requireOracleHarnessFailure(t, result, "schema.probe_invalid")
	})

	t.Run("replay retains orphan records", func(t *testing.T) {
		mutated := cloneOracleFacts(facts)
		for i, x := range mutated {
			if e, ok := x.event.(conversation.WorkEvent); ok && x.record.Sequence > baseline.Start.Sequence && e.Event.Change != nil {
				e.Event.Change.ProgressReports = append(e.Event.Change.ProgressReports, work.WorkProgressReport{ID: "orphan", WorkID: f.Original.ID})
				mutated[i].event = e
				break
			}
		}
		result := oracleResult(baseline)
		gradeSchema(&result, scenario, f, facts)
		addSchemaReplayAssertion(&result, f, facts, mutated)
		requireOracleHarnessFailure(t, result, "trace.replay_consistency")
	})
}

func TestSchemaOracleChecksVerdictAndReportBindings(t *testing.T) {
	for _, id := range []string{"schema-audit-fail-findings", "schema-audit-pass-findings", "schema-progress-objective"} {
		t.Run(id, func(t *testing.T) {
			scenario, f, baseline, facts := schemaOracleFixture(t, id)
			mutated := cloneOracleFacts(facts)
			for i, x := range mutated {
				if e, ok := x.event.(conversation.WorkEvent); ok && x.record.Sequence > baseline.Start.Sequence && e.Event.Change != nil {
					if len(e.Event.Change.Audits) > 0 {
						e.Event.Change.Audits[0].SubmissionID = "wrong-submission"
					}
					if len(e.Event.Change.ProgressReports) > 0 {
						e.Event.Change.ProgressReports[0].AssignedAtRevision++
					}
					mutated[i].event = e
				}
			}
			result := oracleResult(baseline)
			gradeSchema(&result, scenario, f, mutated)
			requireOracleHarnessFailure(t, result, "schema.transition")
		})
	}
}

func runSchemaResponses(t *testing.T, build func(fixture) []provider.Response) Result {
	t.Helper()
	return runSchemaScenarioResponses(t, "schema-audit-repair-field", build)
}

func runSchemaScenarioResponses(t *testing.T, id string, build func(fixture) []provider.Response) Result {
	t.Helper()
	opts := testOptions(t)
	opts.ScenarioIDs = []string{id}
	var script *fixtureScript
	opts.Provider = testProviderFunc(func(ctx context.Context, request provider.Request, observer provider.Observer) (provider.Response, error) {
		if script == nil {
			body, err := os.ReadFile(filepath.Join(opts.Output, id, "001", "manifest.json"))
			if err != nil {
				return provider.Response{}, err
			}
			var manifest struct {
				Fixture fixture `json:"fixture"`
			}
			if err := json.Unmarshal(body, &manifest); err != nil {
				return provider.Response{}, err
			}
			script = &fixtureScript{responses: build(manifest.Fixture)}
		}
		return script.Submit(ctx, request, observer)
	})
	return trialResult(t, opts)
}

func TestSchemaLiveMultipleArgumentErrorsCanRecover(t *testing.T) {
	result := runSchemaScenarioResponses(t, "schema-audit-kind", func(f fixture) []provider.Response {
		return []provider.Response{
			responseCall("assign_audit", map[string]any{"assignee": f.Auditor, "work_id": f.Original.ID, "expected_revision": f.Original.Revision, "submission_id": f.Submission.ID, "kind": "audit", "audit_id": f.Original.LatestAuditID}),
			{ToolCalls: []provider.ToolCall{adversarialAudit(f)}},
		}
	})
	if result.Outcome != "passed" || !result.Behavior.RecoverySuccess || result.Schema == nil || result.Schema.InvalidCalls != 1 || result.Schema.FirstArgumentsValid {
		t.Fatalf("decoder may report either invalid field while rejecting the whole call: %+v", result)
	}
}

func TestSchemaInvalidMixedControlBatchDoesNotExerciseDecoder(t *testing.T) {
	result := runSchemaResponses(t, func(f fixture) []provider.Response {
		return []provider.Response{
			{ToolCalls: []provider.ToolCall{
				adversarialCall("invalid", "assign_audit", map[string]any{"assignee": f.Auditor, "audit_id": f.Original.LatestAuditID}),
				adversarialCall("wait", "wait_for_input", map[string]any{}),
			}},
			{ToolCalls: []provider.ToolCall{adversarialAudit(f)}},
		}
	})
	if result.Outcome != "passed" || !result.Behavior.RecoverySuccess || result.Schema == nil || result.Schema.InvalidCalls != 1 {
		t.Fatalf("mixed control rejection precedes schema validation and can recover: %+v", result)
	}
	for _, assertion := range result.Assertions {
		if assertion.ID == "schema.rejected" {
			t.Fatal("batch guard must not be reported as exercised schema guard")
		}
	}
}

func TestSchemaFirstSelectionSurvivesRecovery(t *testing.T) {
	result := runSchemaResponses(t, func(f fixture) []provider.Response {
		return []provider.Response{
			responseCall("assign_repair", map[string]any{"assignee": f.Implementor, "work_id": f.Original.ID, "expected_revision": f.Original.Revision, "audit_id": f.Original.LatestAuditID}),
			{ToolCalls: []provider.ToolCall{adversarialAudit(f)}},
		}
	})
	if result.Outcome != "passed" || !result.Behavior.RecoverySuccess || result.Schema == nil || result.Schema.Attempts != 2 || result.Schema.FirstToolCorrect || !result.Schema.FirstArgumentsValid || result.Schema.InvalidCalls != 0 {
		t.Fatalf("schema-valid wrong operation must retain selection failure after recovery: %+v", result)
	}
}

func TestSchemaSuccessfulOperationDoesNotHideSameBatchEffects(t *testing.T) {
	result := runSchemaResponses(t, func(f fixture) []provider.Response {
		return []provider.Response{{ToolCalls: []provider.ToolCall{
			adversarialAudit(f),
			// The extra action must land to exercise the grader, so it carries
			// no revision that the graded operation could invalidate first.
			adversarialCall("plan", "create_plan", map[string]any{"title": "Second plan in the same batch", "steps": []any{map[string]any{"title": "Do it again"}}}),
		}}}
	})
	if result.Outcome != "failed" || !result.Behavior.Scorable || result.Harness.Passed != result.Harness.Total || result.ToolCalls != 2 {
		t.Fatalf("same-batch effect must be a model failure with conforming harness: %+v", result)
	}
	assertFailed(t, result, "effects.no_extra_actions")
	assertFailed(t, result, "outcome.exact_state")
}

func TestSchemaOracleAcceptsEquivalentJSONIntegers(t *testing.T) {
	for _, spelling := range []string{"%d.0", "%de0"} {
		t.Run(spelling, func(t *testing.T) {
			result := runSchemaResponses(t, func(f fixture) []provider.Response {
				call := adversarialAudit(f)
				var arguments map[string]json.RawMessage
				if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
					t.Fatal(err)
				}
				arguments["expected_revision"] = json.RawMessage(fmt.Sprintf(spelling, f.Original.Revision))
				call.Arguments, _ = json.Marshal(arguments)
				return []provider.Response{{ToolCalls: []provider.ToolCall{call}}}
			})
			if result.Outcome != "passed" || !result.Behavior.CleanSuccess || result.Schema == nil || !result.Schema.FirstArgumentsValid {
				t.Fatalf("JSON integer notation must not change semantic grading: %+v", result)
			}
		})
	}
}

func TestSchemaProgressRequiresRequestedPositionContent(t *testing.T) {
	result := runSchemaScenarioResponses(t, "schema-progress-objective", func(f fixture) []provider.Response {
		return []provider.Response{responseCall("report_work_progress", work.ReportWorkProgressRequest{
			WorkID: f.Schema.Target.ID,

			Position: &work.WorkPosition{Objective: f.Schema.ExpectedPosition.Objective},
		})}
	})
	if result.Outcome != "failed" || !result.Behavior.Scorable || result.Schema == nil || !result.Schema.FirstArgumentsValid {
		t.Fatalf("schema-valid progress still needs the requested note and next step: %+v", result)
	}
	assertFailed(t, result, "outcome.one_operation")
}
