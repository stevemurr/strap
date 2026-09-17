package interaction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
)

func TestReportReadsCanonicalRecordsAndWritesSummaries(t *testing.T) {
	dir := t.TempDir()
	report := Report{Version: 1, Mode: Scripted, Results: []Result{{
		ScenarioID: "audit-independent", Trial: 1, Mode: Scripted, Outcome: "passed",
		Harness: Score{Passed: 3, Total: 3}, Behavior: Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: true},
	}}}
	writeCanonicalReport(t, dir, report)
	// An earlier derived report is never the source of truth.
	writeReportTestFile(t, filepath.Join(dir, "report.json"), `{"version":99}`)
	got, err := ReadReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, report) {
		t.Fatalf("read report = %#v; want %#v", got, report)
	}
	if err := WriteReport(dir, got); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, report) {
		t.Fatalf("written JSON = %#v; want %#v", decoded, report)
	}
	markdown, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scripted diagnostics", "not live model performance", "Assertions passed/exercised: 3/3", "Input tokens: unknown"} {
		if !strings.Contains(string(markdown), want) {
			t.Errorf("Markdown missing %q:\n%s", want, markdown)
		}
	}
	if strings.Contains(string(markdown), "live model rates") {
		t.Error("scripted report is labeled as model rates")
	}
}

func TestReadReportRejectsCorruptCanonicalRecords(t *testing.T) {
	valid := `{"scenario_id":"audit-independent","trial":1,"mode":"scripted"}`
	for _, tc := range []struct {
		name string
		run  string
		rows string
		want string
	}{
		{"truncated row", `{"version":1,"mode":"scripted"}`, valid + "\n{\"scenario_id\":", "line 2"},
		{"multiple values", `{"version":1,"mode":"scripted"}`, valid + valid, "line 1"},
		{"blank row", `{"version":1,"mode":"scripted"}`, valid + "\n\n", "line 2"},
		{"null row", `{"version":1,"mode":"scripted"}`, "null\n", "scenario_id"},
		{"missing trial", `{"version":1,"mode":"scripted"}`, `{"scenario_id":"audit-independent","mode":"scripted"}`, "trial must be positive"},
		{"wrong mode", `{"version":1,"mode":"live"}`, valid, "does not match"},
		{"duplicate trial", `{"version":1,"mode":"scripted"}`, valid + "\n" + valid + "\n", "duplicate scenario/trial"},
		{"missing row mode", `{"version":1,"mode":"scripted"}`, `{"scenario_id":"audit-independent","trial":1}`, "does not match"},
		{"bad score", `{"version":1,"mode":"scripted"}`, `{"scenario_id":"audit-independent","trial":1,"mode":"scripted","harness":{"passed":3,"total":2}}`, "invalid harness score"},
		{"bad schema counts", `{"version":1,"mode":"scripted"}`, `{"scenario_id":"schema-audit-required","trial":1,"mode":"scripted","schema":{"attempts":1,"invalid_calls":2}}`, "invalid schema counts"},
		{"unattempted schema score", `{"version":1,"mode":"scripted"}`, `{"scenario_id":"schema-audit-required","trial":1,"mode":"scripted","schema":{"attempts":0,"first_tool_correct":true}}`, "requires an attempted operation"},
		{"truncated metadata", `{"version":1`, valid, "decode run.json"},
		{"unknown version", `{"version":2,"mode":"scripted"}`, valid, "unsupported report version"},
		{"missing metadata mode", `{"version":1}`, valid, "invalid report mode"},
		{"negative planned trials", `{"version":1,"mode":"scripted","planned_trials":-1}`, valid, "planned trials must be nonnegative"},
		{"more results than planned", `{"version":1,"mode":"scripted","planned_trials":1}`, valid + "\n" + `{"scenario_id":"audit-independent","trial":2,"mode":"scripted"}`, "result count exceeds planned trials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeReportTestFile(t, filepath.Join(dir, "run.json"), tc.run)
			writeReportTestFile(t, filepath.Join(dir, "results.jsonl"), tc.rows)
			got, err := ReadReport(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
			if len(got.Results) != 0 {
				t.Fatal("returned partial records after corruption")
			}
		})
	}
}

func TestReadReportAcceptsLastRowWithoutNewlineAndRepeatedScenario(t *testing.T) {
	dir := t.TempDir()
	writeReportTestFile(t, filepath.Join(dir, "run.json"), `{"version":1,"mode":"live"}`)
	writeReportTestFile(t, filepath.Join(dir, "results.jsonl"), "{\"scenario_id\":\"audit-independent\",\"trial\":1,\"mode\":\"live\"}\r\n{\"scenario_id\":\"audit-independent\",\"trial\":2,\"mode\":\"live\"}")
	report, err := ReadReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 2 || report.Results[1].Trial != 2 {
		t.Fatalf("results = %#v", report.Results)
	}
}

func TestReportDenominatorsFailureEvidenceAndUnknownUsage(t *testing.T) {
	dir := t.TempDir()
	tokens := int64(42)
	report := Report{Version: 1, Mode: Live, Results: []Result{
		{ScenarioID: "audit-independent", Trial: 1, Mode: Live, Outcome: "passed",
			Harness: Score{Passed: 4, Total: 4}, Behavior: Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: true},
			ModelCalls: 2, ToolCalls: 3, Duration: 2 * time.Second, InputTokens: &tokens},
		{ScenarioID: "audit-independent", Trial: 2, Mode: Live, Outcome: "passed",
			Harness: Score{Passed: 5, Total: 5}, Behavior: Behavior{Scorable: true, OutcomeCorrect: true, RecoverySuccess: true, RejectedCalls: 1, OutputErrors: 1},
			ModelCalls: 3, ToolCalls: 4, Duration: 3 * time.Second},
		{ScenarioID: "audit-independent", Trial: 3, Mode: Live, Outcome: "error", Error: "capture lost", ErrorClass: "capture",
			Harness: Score{Passed: 1, Total: 2}, Behavior: Behavior{Scorable: false, OutcomeCorrect: true, CleanSuccess: true, RecoverySuccess: true, UnknownToolErrors: 1},
			ModelCalls: 1, ToolCalls: 1, Duration: time.Second, Trace: filepath.Join(dir, "trial 3", "trace.jsonl"), Manifest: filepath.Join(dir, "trial 3", "manifest.json"),
			Start: eventlog.Cursor{Session: "session-a", Sequence: 20}, Through: eventlog.Cursor{Session: "session-a", Sequence: 30},
			Assertions: []Assertion{{ID: "capture.complete", Track: "harness", Passed: false, Expected: "checking", Actual: "submitted", Cursor: eventlog.Cursor{Session: "session-a", Sequence: 29}, Invocation: "call-7"}}},
	}}
	if err := WriteReport(dir, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	markdown := string(data)
	for _, want := range []string{
		"Behavior: live model rates", "Assertions passed/exercised: 10/11", "Scorable trials: 2/3; excluded: 1",
		"| Correct outcome | 2/2 |", "| Clean success | 1/2 |", "| Recovered success | 1/2 |",
		"Trials with errors: 1/3 completed trials", "| capture | 1/3 |",
		"Rejected tool calls: 1/8 tool calls", "Unknown tool errors: 1/8 tool calls", "Output errors: 1/6 model calls", "Model calls: 6", "Summed trial duration: 6s",
		"Input tokens: 42 known (1/3 trials reported; 2 unknown)", "Output tokens: unknown (0/3 trials reported)",
		"[Trace](trial%203/trace.jsonl)", "[Manifest](trial%203/manifest.json)", "Cursors: session-a:20 → session-a:30",
		"capture.complete", "cursor session-a:29, invocation call-7", `expected "checking"; actual "submitted"`, "Behavior: unscorable",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestReportEmptyDenominators(t *testing.T) {
	markdown := reportMarkdown(t.TempDir(), Report{Version: 1, Mode: Live})
	for _, want := range []string{"Assertions passed/exercised: 0/0", "Scorable trials: 0/0", "No behavior rate is available", "No trial results"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestReportSchemaFirstDecisionDenominatorsAndRecovery(t *testing.T) {
	report := Report{Version: 1, Mode: Live, Results: []Result{
		{ScenarioID: "schema-audit-required", Trial: 1, Mode: Live,
			Behavior: Behavior{Scorable: true, OutcomeCorrect: true, RecoverySuccess: true},
			Schema:   &SchemaBehavior{Attempts: 2, InvalidCalls: 1, FirstToolCorrect: true}},
		{ScenarioID: "schema-audit-required", Trial: 2, Mode: Live,
			Behavior: Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: true},
			Schema:   &SchemaBehavior{Attempts: 1, FirstToolCorrect: true, FirstArgumentsValid: true}},
		{ScenarioID: "schema-audit-required", Trial: 3, Mode: Live,
			Behavior: Behavior{Scorable: true}, Schema: &SchemaBehavior{}},
		{ScenarioID: "schema-audit-required", Trial: 4, Mode: Live,
			Schema: &SchemaBehavior{Attempts: 1, FirstToolCorrect: true, FirstArgumentsValid: true}},
		{ScenarioID: "schema-audit-required", Trial: 5, Mode: Live,
			Behavior: Behavior{Scorable: true},
			Schema:   &SchemaBehavior{Attempts: 1, FirstArgumentsValid: true}},
		{ScenarioID: "audit-independent", Trial: 1, Mode: Live,
			Behavior: Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: true}},
	}}
	dir := t.TempDir()
	writeCanonicalReport(t, dir, report)
	got, err := ReadReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, report) {
		t.Fatalf("schema scores did not survive canonical records: %#v", got)
	}
	markdown := reportMarkdown(dir, got)
	for _, want := range []string{
		"Schema trials: 5. First decisions scored: 3/5; no operation attempted: 1; excluded: 1",
		"| Correct first tool | 2/3 |", "| Valid first arguments | 2/3 |",
		"Recorded invalid contract calls: 1/5 attempted operations across all schema trials",
		"| Recovered success | 1/5 |", "| Correct outcome | 3/5 |",
		"Schema first decision: correct tool=true, valid arguments=false",
		"Schema first decision: correct tool=false, valid arguments=true",
		"Schema first decision: no operation attempted", "Schema first decision: excluded (unscorable trial)",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("missing %q:\n%s", want, markdown)
		}
	}
}

func TestReportSchemaWithNoScorableAttempt(t *testing.T) {
	report := Report{Version: 1, Mode: Live, Results: []Result{{
		ScenarioID: "schema-audit-required", Trial: 1, Mode: Live,
		Behavior: Behavior{Scorable: true}, Schema: &SchemaBehavior{},
	}}}
	markdown := reportMarkdown(t.TempDir(), report)
	for _, want := range []string{"First decisions scored: 0/1; no operation attempted: 1", "| Valid first arguments | 0/0 |", "No first-decision rate is available"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("missing %q:\n%s", want, markdown)
		}
	}
}

func TestReportIncompleteRunRetainsPlannedDenominator(t *testing.T) {
	dir := t.TempDir()
	report := Report{Version: 1, Mode: Live, PlannedTrials: 3, Results: []Result{
		{ScenarioID: "audit-independent", Trial: 1, Mode: Live, Outcome: "passed", Harness: Score{Passed: 1, Total: 1}, Behavior: Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: true}},
		{ScenarioID: "audit-independent", Trial: 2, Mode: Live, Outcome: "passed", Harness: Score{Passed: 1, Total: 1}, Behavior: Behavior{Scorable: true, OutcomeCorrect: true, CleanSuccess: true}},
	}}
	writeCanonicalReport(t, dir, report)
	got, err := ReadReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlannedTrials != 3 || len(got.Results) != 2 {
		t.Fatalf("missing final trial silently changed planned count: %#v", got)
	}
	if err := WriteReport(dir, got); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Completed/planned trials: 2/3", "Incomplete run: missing 1 of 3 planned results", "Scores below cover completed trials only", "Scorable trials: 2/2"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("incomplete run report missing %q:\n%s", want, data)
		}
	}
	// Writers enforce the declared upper bound too, without forbidding partial runs.
	got.PlannedTrials = 1
	if err := WriteReport(dir, got); err == nil || !strings.Contains(err.Error(), "exceeds planned trials") {
		t.Fatalf("too many results write error = %v", err)
	}
}

func TestReportPropagatesFileErrors(t *testing.T) {
	report := Report{Version: 1, Mode: Scripted}
	for _, name := range []string{"report.json", "report.md"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := WriteReport(dir, report); err == nil || !strings.Contains(err.Error(), "write "+name) {
				t.Fatalf("error = %v; want write %s failure", err, name)
			}
		})
	}
	if _, err := ReadReport(t.TempDir()); err == nil || !strings.Contains(err.Error(), "read run.json") {
		t.Fatalf("missing run.json error = %v", err)
	}
	dir := t.TempDir()
	writeReportTestFile(t, filepath.Join(dir, "run.json"), `{"version":1,"mode":"scripted"}`)
	if _, err := ReadReport(dir); err == nil || !strings.Contains(err.Error(), "read results.jsonl") {
		t.Fatalf("missing results.jsonl error = %v", err)
	}
}

func writeCanonicalReport(t *testing.T, dir string, report Report) {
	t.Helper()
	metadata, err := json.Marshal(Report{Version: report.Version, Mode: report.Mode, PlannedTrials: report.PlannedTrials})
	if err != nil {
		t.Fatal(err)
	}
	writeReportTestFile(t, filepath.Join(dir, "run.json"), string(metadata))
	var rows strings.Builder
	for _, result := range report.Results {
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		rows.Write(data)
		rows.WriteByte('\n')
	}
	writeReportTestFile(t, filepath.Join(dir, "results.jsonl"), rows.String())
}

func writeReportTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
