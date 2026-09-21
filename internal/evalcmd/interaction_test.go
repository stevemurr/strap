package evalcmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eval/interaction"
)

func TestInteractionList(t *testing.T) {
	var out bytes.Buffer
	if err := Main(context.Background(), []string{"interaction", "list"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range interaction.List() {
		if !strings.Contains(out.String(), fmt.Sprintf("%s\tv%d\t", scenario.ID, scenario.Version)) {
			t.Fatalf("missing scenario %s: %s", scenario.ID, out.String())
		}
	}
	for _, args := range [][]string{{"interaction", "unknown"}, {"interaction", "list", "extra"}, {"interaction", "report"}, {"interaction", "run", "extra"}, {"interaction", "run", "-mode", "unknown"}} {
		if err := Main(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestInteractionOptions(t *testing.T) {
	opts, err := interactionOptions([]string{"-config", filepath.Join(t.TempDir(), "missing.json")}, io.Discard)
	if err != nil {
		t.Fatalf("scripted run consulted model catalog: %v", err)
	}
	if opts.Mode != interaction.Scripted || opts.Profile != "scripted" || opts.Repetitions != 1 || opts.MaxCalls != 8 || opts.MaxToolCalls != 24 || opts.Timeout != 3*time.Minute {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
	if !strings.HasPrefix(opts.Output, filepath.Join("eval", "results", "interaction_")) {
		t.Fatalf("unexpected output directory: %s", opts.Output)
	}

	catalog := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(catalog, []byte(`{"default":"test","models":{"test":{"model":"catalog-model","base_url":"http://127.0.0.1:1","timeout":"45s","generation":{"temperature":0.8}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err = interactionOptions([]string{"-mode", "live", "-config", catalog, "-scenario", "audit-independent, audit-stale-revision", "-repeat", "2", "-max-calls", "5", "-max-tool-calls", "10", "-timeout", "7s", "-model-timeout", "2s", "-model", "override", "-temperature", "0.2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Mode != interaction.Live || opts.Profile != "test" || opts.Config.Model.Model != "override" || opts.Config.Model.Timeout != 2*time.Second || opts.Timeout != 7*time.Second {
		t.Fatalf("model and trial flags did not remain independent: %+v", opts)
	}
	if opts.Repetitions != 2 || opts.MaxCalls != 5 || opts.MaxToolCalls != 10 || strings.Join(opts.ScenarioIDs, ",") != "audit-independent,audit-stale-revision" {
		t.Fatalf("selection and limits not applied: %+v", opts)
	}
	if opts.Config.Model.Generation.Temperature == nil || *opts.Config.Model.Generation.Temperature != 0.2 {
		t.Fatalf("model flag did not override catalog: %+v", opts.Config.Model.Generation)
	}
	opts, err = interactionOptions([]string{"-mode", "live", "-config", catalog, "-timeout", "7s"}, io.Discard)
	if err != nil || opts.Config.Model.Timeout != 45*time.Second || opts.Timeout != 7*time.Second {
		t.Fatalf("catalog request timeout lost: %+v, %v", opts, err)
	}
	opts, err = interactionOptions([]string{"-mode", "live", "-config", catalog, "-backend", "chatcompletions"}, io.Discard)
	if err != nil || opts.Config.Model.Generation.Temperature != nil {
		t.Fatalf("chatcompletions override retained catalog generation policy: %+v, %v", opts.Config.Model.Generation, err)
	}
}

func TestInteractionScriptedRunAndReport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	var out bytes.Buffer
	args := []string{"interaction", "run", "-scenario", "audit-independent,audit-wrong-assignee", "-repeat", "2", "-out", dir, "-config", filepath.Join(t.TempDir(), "missing.json")}
	if err := Main(context.Background(), args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "4/4 interaction trials passed (mode=scripted)") || !strings.Contains(out.String(), "script behavior:") || !strings.Contains(out.String(), "clean=false") || !strings.Contains(out.String(), "reports:") {
		t.Fatal(out.String())
	}
	report, err := interaction.ReadReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 4 {
		t.Fatalf("got %d trials, expected 4", len(report.Results))
	}
	for _, file := range []string{"report.md", "report.json"} {
		if err := os.Remove(filepath.Join(dir, file)); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	if err := Main(context.Background(), []string{"interaction", "report", dir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "4/4 interaction trials passed") {
		t.Fatal(out.String())
	}
	for _, file := range []string{"report.md", "report.json"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Fatal(err)
		}
	}
	if err := Main(context.Background(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("accepted nonempty output directory")
	}
}

func TestInteractionInvalidRunFlags(t *testing.T) {
	for _, args := range [][]string{{"-repeat", "0"}, {"-max-calls", "-1"}, {"-max-tool-calls", "0"}, {"-timeout", "0s"}, {"-scenario", "missing-scenario"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			args = append([]string{"interaction", "run", "-out", filepath.Join(t.TempDir(), "run")}, args...)
			if err := Main(context.Background(), args, io.Discard, io.Discard); err == nil {
				t.Fatalf("accepted %v", args)
			}
		})
	}
}

func TestInteractionFailedTrialPreservesReportAndExitStatus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	for _, args := range [][]string{
		{"interaction", "run", "-scenario", "audit-wrong-assignee", "-max-calls", "1", "-out", dir},
		{"interaction", "report", dir},
	} {
		var out bytes.Buffer
		err := Main(context.Background(), args, &out, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "1/1 interaction trials did not pass") {
			t.Fatalf("failed trial exit status lost: %v", err)
		}
		if !strings.Contains(out.String(), "0/1 interaction trials passed") || !strings.Contains(out.String(), "reports:") {
			t.Fatalf("failed trial omitted report summary: %s", out.String())
		}
	}
}

func TestInteractionReportRejectsIncompleteRun(t *testing.T) {
	dir := t.TempDir()
	for file, data := range map[string]string{
		"run.json":      `{"version":1,"mode":"scripted"}`,
		"results.jsonl": "",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := Main(context.Background(), []string{"interaction", "report", dir}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "incomplete: no completed trial results") {
		t.Fatalf("empty run did not fail as incomplete: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("incomplete run emitted a success summary: %s", out.String())
	}
}

func TestInteractionReportRejectsPartialRunWithPassingResults(t *testing.T) {
	dir := t.TempDir()
	for file, data := range map[string]string{
		"run.json":      `{"version":1,"mode":"scripted","planned_trials":2}`,
		"results.jsonl": `{"scenario_id":"audit-independent","trial":1,"mode":"scripted","outcome":"passed"}` + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := Main(context.Background(), []string{"interaction", "report", dir}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "incomplete: 1/2 trials recorded") {
		t.Fatalf("partial run did not fail as incomplete: %v", err)
	}
	if !strings.Contains(out.String(), "1/2 trials recorded") || !strings.Contains(out.String(), "reports:") {
		t.Fatalf("partial run omitted progress and report paths: %s", out.String())
	}
}

func TestInteractionOutcomeExitSemantics(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    interaction.Result
		wantError bool
	}{
		{"expected rejection is a conformance pass", interaction.Result{Mode: interaction.Scripted, Outcome: "passed", Behavior: interaction.Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: false, RecoverySuccess: true}}, false},
		{"model recovery is a pass with separate clean score", interaction.Result{Mode: interaction.Live, Outcome: "passed", Behavior: interaction.Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: false, RecoverySuccess: true}}, false},
		{"failed", interaction.Result{Outcome: "failed"}, true},
		{"provider error", interaction.Result{Outcome: "provider_error"}, true},
		{"unknown outcome", interaction.Result{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := interactionOutcomeError(interaction.Report{Results: []interaction.Result{tc.result}})
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, want error %t", err, tc.wantError)
			}
			var out bytes.Buffer
			printInteractionResult(&out, tc.result)
			label := "script behavior:"
			if tc.result.Mode == interaction.Live {
				label = "model behavior:"
			}
			if !strings.Contains(out.String(), label) {
				t.Fatalf("missing separate behavior label: %s", out.String())
			}
		})
	}
}

func TestInteractionSchemaScoresRemainSeparateFromRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scorable bool
		schema   *interaction.SchemaBehavior
		want     string
	}{
		{"recovered", true, &interaction.SchemaBehavior{Attempts: 2, InvalidCalls: 1, FirstToolCorrect: true}, "schema: first_tool=true first_arguments=false invalid=1/2"},
		{"unattempted", true, &interaction.SchemaBehavior{}, "schema: first_decision=unattempted invalid=0/0"},
		{"excluded", false, &interaction.SchemaBehavior{Attempts: 1, FirstToolCorrect: true, FirstArgumentsValid: true}, "schema: first_decision=excluded invalid=0/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := interaction.Result{
				Mode: interaction.Live, Outcome: "passed",
				Behavior: interaction.Behavior{Scorable: tc.scorable, OutcomeCorrect: true, RecoverySuccess: true},
				Schema:   tc.schema,
			}
			var out bytes.Buffer
			printInteractionResult(&out, result)
			if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "recovery=true") {
				t.Fatalf("schema and recovery scores not separately shown: %s", out.String())
			}
			if err := interactionOutcomeError(interaction.Report{Results: []interaction.Result{result}}); err != nil {
				t.Fatalf("first-call error changed successful recovery exit status: %v", err)
			}
		})
	}
}
