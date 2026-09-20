package interaction

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

func TestRevisionRaceOracleCounterexamples(t *testing.T) {
	scenario, f, baseline, facts := raceOracleFixture(t)
	control := raceOracleResult(baseline)
	grade(&control, scenario, f, facts)
	if !control.Behavior.Scorable || !control.Behavior.RecoverySuccess || control.Behavior.CleanSuccess {
		t.Fatalf("valid forced race did not pass recovery: %+v", control)
	}

	t.Run("missing intervention is a model failure", func(t *testing.T) {
		result := raceOracleResult(baseline)
		result.RevisionRace = nil
		grade(&result, scenario, f, facts)
		assertFailed(t, result, "race.triggered")
		if !result.Behavior.Scorable || result.Behavior.OutcomeCorrect {
			t.Fatalf("absent challenge must remain a scored failure: %+v", result)
		}
	})

	t.Run("missing conflict is not recovery", func(t *testing.T) {
		var filtered []fact
		for _, x := range facts {
			if e, ok := x.event.(conversation.ToolEvent); ok && e.Agent == f.Root && e.Activity.Call.ID == baseline.RevisionRace.TriggerCallID {
				continue
			}
			filtered = append(filtered, x)
		}
		result := raceOracleResult(baseline)
		grade(&result, scenario, f, filtered)
		assertFailed(t, result, "race.revision_rejected")
		if !result.Behavior.Scorable || result.Behavior.RecoverySuccess {
			t.Fatalf("missing conflict must remain a scored failure: %+v", result)
		}
	})

	t.Run("conflict receipt alone supplies current revision", func(t *testing.T) {
		result := raceOracleResult(baseline)
		grade(&result, scenario, f, withoutRaceRefresh(facts, result))
		if !result.Behavior.RecoverySuccess {
			t.Fatalf("authoritative conflict revision should suffice: %+v", result)
		}
	})

	t.Run("guessing revision without receipt or read fails", func(t *testing.T) {
		result := raceOracleResult(baseline)
		mutated := withoutRaceRefresh(facts, result)
		for i, x := range mutated {
			if e, ok := x.event.(conversation.ToolEvent); ok && e.Activity.Call.ID == result.RevisionRace.TriggerCallID && e.Activity.Err != nil {
				e.Activity.Err = fmt.Errorf("%w: expected_revision differs", work.ErrConflict)
				mutated[i].event = e
			}
		}
		grade(&result, scenario, f, mutated)
		assertFailed(t, result, "race.recovered_with_current_revision")
		if !result.Behavior.Scorable || result.Behavior.RecoverySuccess {
			t.Fatalf("unsupported revision should be a scored failure: %+v", result)
		}
	})

	t.Run("provider call IDs may repeat across responses", func(t *testing.T) {
		result := raceOracleResult(baseline)
		mutated := cloneOracleFacts(facts)
		for i, x := range mutated {
			if e, ok := x.event.(conversation.ToolEvent); ok && e.Activity.Call.Name == "assign_audit" && x.record.Sequence > result.RevisionRace.Through.Sequence {
				e.Activity.Call.ID = result.RevisionRace.TriggerCallID
				mutated[i].event = e
			}
		}
		grade(&result, scenario, f, mutated)
		if !result.Behavior.RecoverySuccess {
			t.Fatalf("host invocations must distinguish repeated provider IDs: %+v", result)
		}
	})

	t.Run("actor action inside host interval is still scored", func(t *testing.T) {
		result := raceOracleResult(baseline)
		race := *result.RevisionRace
		result.RevisionRace = &race
		mutated := cloneOracleFacts(facts)
		for i, x := range mutated {
			if x.record.Sequence <= race.Before.Sequence {
				continue
			}
			start := x.record
			start.Kind, start.Agent = "tool", string(f.Root)
			finish := start
			finish.Sequence++
			activity := agent.ToolActivity{InvocationID: "unexpected-interleaved-invocation", Call: provider.ToolCall{ID: "unexpected-interleaved-call", Name: "rename_plan", Arguments: json.RawMessage(`{"input":{}}`)}, StartedAt: time.Now()}
			startEvent := conversation.ToolEvent{Agent: f.Root, Activity: activity}
			activity.FinishedAt = activity.StartedAt.Add(time.Millisecond)
			finishEvent := conversation.ToolEvent{Agent: f.Root, Activity: activity}
			for j := i; j < len(mutated); j++ {
				mutated[j].record.Sequence += 2
			}
			inserted := []fact{{record: start, event: startEvent}, {record: finish, event: finishEvent}}
			mutated = append(mutated[:i], append(inserted, mutated[i:]...)...)
			race.Through.Sequence += 2
			result.Through.Sequence += 2
			break
		}
		grade(&result, scenario, f, mutated)
		assertFailed(t, result, "effects.no_extra_actions")
		if !result.Behavior.Scorable || result.Behavior.OutcomeCorrect {
			t.Fatalf("interleaved actor call must not be exempted as host interference: %+v", result)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*work.Change)
	}{
		{"extra original change", func(c *work.Change) {
			for i := range c.Works {
				if c.Works[i].ID == f.Original.ID {
					c.Works[i].Note = "An unrelated field changed during the intervention."
				}
			}
		}},
		{"extra immutable record", func(c *work.Change) {
			sub := f.Submission.Clone()
			sub.Summary = "Unexpected rewritten evidence."
			c.Submissions = append(c.Submissions, sub)
		}},
		{"extra progress record", func(c *work.Change) {
			c.ProgressReports = append(c.ProgressReports, work.WorkProgressReport{})
		}},
		{"extra audit work", func(c *work.Change) {
			audit := baseline.RevisionRace.CancelledAudit.Clone()
			audit.ID = "unexpected-audit"
			c.Works = append(c.Works, audit)
		}},
		{"incorrect intermediate revision", func(c *work.Change) {
			for i := range c.Works {
				if c.Works[i].ID == f.Original.ID {
					c.Works[i].Revision++
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := raceOracleResult(baseline)
			mutated := cloneOracleFacts(facts)
			changed := false
			for i, x := range mutated {
				if e, ok := x.event.(conversation.WorkEvent); ok && x.record.Sequence > result.RevisionRace.Before.Sequence && x.record.Sequence <= result.RevisionRace.Through.Sequence && e.Event.Change != nil {
					tc.mutate(e.Event.Change)
					mutated[i].event = e
					changed = true
					break
				}
			}
			if !changed {
				t.Fatal("missing intervention change")
			}
			grade(&result, scenario, f, mutated)
			requireOracleHarnessFailure(t, result, "race.exact_intervention")
		})
	}
}

func withoutRaceRefresh(facts []fact, result Result) []fact {
	var filtered []fact
	for _, x := range facts {
		if e, ok := x.event.(conversation.ToolEvent); ok && e.Activity.Call.Name == "get_work" && x.record.Sequence > result.RevisionRace.Through.Sequence {
			continue
		}
		filtered = append(filtered, x)
	}
	return filtered
}

func raceOracleResult(baseline Result) Result {
	result := oracleResult(baseline)
	result.Mode = Live
	result.RevisionRace = baseline.RevisionRace
	return result
}

func raceOracleFixture(t *testing.T) (Scenario, fixture, Result, []fact) {
	t.Helper()
	dir := t.TempDir()
	report, err := Run(context.Background(), Options{Mode: Scripted, Output: dir, ScenarioIDs: []string{"audit-revision-race"}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Outcome != "passed" {
		t.Fatalf("passing race fixture required, got %#v", report.Results)
	}
	result := report.Results[0]
	data, err := os.ReadFile(filepath.Join(dir, result.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Scenario Scenario `json:"scenario"`
		Fixture  fixture  `json:"fixture"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
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
