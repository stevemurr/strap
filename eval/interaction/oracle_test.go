package interaction

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/work"
)

// Mutate domain facts from an actual passing interaction. These counterexamples
// check that the oracle detects runtime faults even when the actor made the
// correct request and a successful tool result was recorded.
func TestOracleRejectsCorruptedDomainFacts(t *testing.T) {
	scenario, f, baseline, facts := oracleFixture(t, "audit-wrong-assignee")
	control := oracleResult(baseline)
	grade(&control, scenario, f, facts)
	if control.Harness.Passed != control.Harness.Total || !control.Behavior.Scorable || !control.Behavior.OutcomeCorrect || control.Behavior.CleanSuccess || !control.Behavior.RecoverySuccess {
		t.Fatalf("uncorrupted rejected-then-recovered interaction graded incorrectly: %#v", control)
	}
	for _, id := range []string{"audit-old-submission", "audit-stale-revision"} {
		t.Run("wrong declared guard "+id, func(t *testing.T) {
			wrongScenario := scenario
			wrongScenario.ID = id
			result := oracleResult(baseline)
			result.ScenarioID = id
			grade(&result, wrongScenario, f, facts)
			requireOracleHarnessFailure(t, result, "scenario.guard_exercised")
		})
	}
	for _, tc := range []struct {
		name      string
		assertion string
		mutate    func(*work.Change)
	}{
		{
			name: "wrong audit submission binding",
			mutate: func(change *work.Change) {
				for i := range change.Works {
					if change.Works[i].Kind == work.AuditWork {
						change.Works[i].SubjectSubmissionID = f.PreviousSubmission
					}
				}
			},
		},
		{
			name: "wrong audit assignee binding",
			mutate: func(change *work.Change) {
				for i := range change.Works {
					if change.Works[i].Kind == work.AuditWork {
						change.Works[i].Assignee = f.Implementor
					}
				}
			},
		},
		{
			name: "changed submission record", assertion: "records.immutable",
			mutate: func(change *work.Change) {
				submission := f.Submission.Clone()
				submission.Summary = "A runtime bug rewrote the submitted evidence."
				change.Submissions = append(change.Submissions, submission)
			},
		},
		{
			name: "plan completed without verdict", assertion: "audit.no_acceptance_without_verdict",
			mutate: func(change *work.Change) {
				plan := f.Plan.Clone()
				plan.Steps[0].Status = work.Completed
				change.Plans = append(change.Plans, plan)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := cloneOracleFacts(facts)
			changed := false
			for i := range mutated {
				e, ok := mutated[i].event.(conversation.WorkEvent)
				if !ok || mutated[i].record.Sequence <= baseline.Start.Sequence || e.Event.Change == nil {
					continue
				}
				for _, w := range e.Event.Change.Works {
					if w.Kind == work.AuditWork && w.SubjectSubmissionID == f.Submission.ID {
						tc.mutate(e.Event.Change)
						mutated[i].event = e
						changed = true
						break
					}
				}
				if changed {
					break
				}
			}
			if !changed {
				t.Fatal("fixture did not contain a new audit assignment change")
			}
			result := oracleResult(baseline)
			grade(&result, scenario, f, mutated)
			requireOracleHarnessFailure(t, result, tc.assertion)
		})
	}

	t.Run("rejected call mutated state before returning error", func(t *testing.T) {
		mutated := cloneOracleFacts(facts)
		inserted := false
		for i, x := range mutated {
			e, ok := x.event.(conversation.ToolEvent)
			if !ok || x.record.Sequence <= baseline.Start.Sequence || e.Agent != f.Root || e.Activity.Err == nil || e.Activity.FinishedAt.IsZero() {
				continue
			}
			original := f.Original.Clone()
			original.Note = "Unexpected mutation during a rejected audit request."
			injected := fact{record: x.record, event: conversation.WorkEvent{Event: work.Event{Change: &work.Change{Works: []work.Work{original}}}}}
			injected.record.Kind = "work"
			// Keep a monotonic synthetic fact stream after inserting the mutation.
			for j := i; j < len(mutated); j++ {
				mutated[j].record.Sequence++
			}
			mutated = append(mutated[:i], append([]fact{injected}, mutated[i:]...)...)
			inserted = true
			break
		}
		if !inserted {
			t.Fatal("fixture did not contain a rejected root tool call")
		}
		result := oracleResult(baseline)
		result.Through.Sequence++
		grade(&result, scenario, f, mutated)
		requireOracleHarnessFailure(t, result, "rejection.atomic")
	})
}

func oracleFixture(t *testing.T, scenarioID string) (Scenario, fixture, Result, []fact) {
	t.Helper()
	dir := t.TempDir()
	report, err := Run(context.Background(), Options{Mode: Scripted, Output: dir, ScenarioIDs: []string{scenarioID}, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Outcome != "passed" {
		t.Fatalf("passing fixture run required, got %#v", report.Results)
	}
	result := report.Results[0]
	manifest, err := readManifest(dir, result.ScenarioID)
	if err != nil {
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

func oracleResult(baseline Result) Result {
	return Result{ScenarioID: baseline.ScenarioID, Trial: baseline.Trial, Mode: baseline.Mode, Start: baseline.Start, Through: baseline.Through}
}

func cloneOracleFacts(facts []fact) []fact {
	copyFacts := append([]fact(nil), facts...)
	for i, x := range copyFacts {
		if e, ok := x.event.(conversation.WorkEvent); ok {
			e.Event = e.Event.Clone()
			copyFacts[i].event = e
		}
	}
	return copyFacts
}

func requireOracleHarnessFailure(t *testing.T, result Result, assertionID string) {
	t.Helper()
	failed := false
	for _, assertion := range result.Assertions {
		if assertion.Track == "harness" && !assertion.Passed && (assertionID == "" || assertion.ID == assertionID) {
			failed = true
			if assertion.Cursor.Sequence == 0 {
				t.Errorf("failed assertion %s has no trace cursor", assertion.ID)
			}
		}
	}
	if !failed || result.Harness.Passed == result.Harness.Total {
		t.Errorf("corrupted domain did not fail harness assertion %q: %#v", assertionID, result.Assertions)
	}
	if result.Behavior.Scorable || result.Behavior.OutcomeCorrect || result.Behavior.CleanSuccess || result.Behavior.RecoverySuccess || result.Outcome == "passed" {
		t.Errorf("runtime corruption contaminated model scores: %#v", result)
	}
}
