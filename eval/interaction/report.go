package interaction

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReadReport reads canonical run metadata and trial records, rather than a
// previously generated report. Invalid records fail the entire read so that a
// damaged run cannot silently acquire a smaller denominator.
func ReadReport(dir string) (Report, error) {
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return Report{}, fmt.Errorf("read run.json: %w", err)
	}
	var metadata struct {
		Version       int  `json:"version"`
		Mode          Mode `json:"mode"`
		PlannedTrials int  `json:"planned_trials"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Report{}, fmt.Errorf("decode run.json: %w", err)
	}
	report := Report{Version: metadata.Version, Mode: metadata.Mode, PlannedTrials: metadata.PlannedTrials, Results: []Result{}}
	if err := validateReport(report); err != nil {
		return Report{}, fmt.Errorf("run.json: %w", err)
	}
	f, err := os.Open(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return Report{}, fmt.Errorf("read results.jsonl: %w", err)
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	seen := map[trialID]bool{}
	for lineNumber := 1; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return Report{}, fmt.Errorf("read results.jsonl line %d: %w", lineNumber, readErr)
		}
		if len(line) == 0 && readErr == io.EOF {
			break
		}
		var result Result
		if err := json.Unmarshal(bytes.TrimSpace(line), &result); err != nil {
			return Report{}, fmt.Errorf("results.jsonl line %d: %w", lineNumber, err)
		}
		if err := validateResult(result, report.Mode, seen); err != nil {
			return Report{}, fmt.Errorf("results.jsonl line %d: %w", lineNumber, err)
		}
		report.Results = append(report.Results, result)
		if report.PlannedTrials > 0 && len(report.Results) > report.PlannedTrials {
			return Report{}, fmt.Errorf("results.jsonl line %d: result count exceeds planned trials %d", lineNumber, report.PlannedTrials)
		}
		if readErr == io.EOF {
			break
		}
	}
	return report, nil
}

// WriteReport writes derived JSON and Markdown summaries. Canonical run.json
// and results.jsonl are owned by the runner and are never overwritten here.
func WriteReport(dir string, report Report) error {
	if err := validateReport(report); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report.json: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write report.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(reportMarkdown(dir, report)), 0o644); err != nil {
		return fmt.Errorf("write report.md: %w", err)
	}
	return nil
}

type trialID struct {
	scenario string
	trial    int
}

func validateReport(report Report) error {
	if report.Version != 1 {
		return fmt.Errorf("unsupported report version %d (want 1)", report.Version)
	}
	if report.Mode != Scripted && report.Mode != Live {
		return fmt.Errorf("invalid report mode %q", report.Mode)
	}
	if report.PlannedTrials < 0 {
		return fmt.Errorf("planned trials must be nonnegative")
	}
	if report.PlannedTrials > 0 && len(report.Results) > report.PlannedTrials {
		return fmt.Errorf("result count %d exceeds planned trials %d", len(report.Results), report.PlannedTrials)
	}
	seen := map[trialID]bool{}
	for i, result := range report.Results {
		if err := validateResult(result, report.Mode, seen); err != nil {
			return fmt.Errorf("result %d: %w", i+1, err)
		}
	}
	return nil
}

func validateResult(result Result, mode Mode, seen map[trialID]bool) error {
	if strings.TrimSpace(result.ScenarioID) == "" || result.Trial < 1 {
		return fmt.Errorf("scenario_id must be nonempty and trial must be positive")
	}
	if result.Mode != mode {
		return fmt.Errorf("mode %q does not match run mode %q", result.Mode, mode)
	}
	id := trialID{result.ScenarioID, result.Trial}
	if seen[id] {
		return fmt.Errorf("duplicate scenario/trial %s/%d", result.ScenarioID, result.Trial)
	}
	seen[id] = true
	if result.Harness.Passed < 0 || result.Harness.Total < result.Harness.Passed {
		return fmt.Errorf("invalid harness score %d/%d", result.Harness.Passed, result.Harness.Total)
	}
	if s := result.Schema; s != nil {
		if s.Attempts < 0 || s.InvalidCalls < 0 || s.InvalidCalls > s.Attempts {
			return fmt.Errorf("invalid schema counts %d invalid/%d attempts", s.InvalidCalls, s.Attempts)
		}
		if s.Attempts == 0 && (s.FirstToolCorrect || s.FirstArgumentsValid) {
			return fmt.Errorf("schema first-decision score requires an attempted operation")
		}
	}
	return nil
}

func reportMarkdown(dir string, report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Interaction evaluation\n\nMode: %s.\n\n", report.Mode)
	if report.PlannedTrials > 0 {
		fmt.Fprintf(&b, "Completed/planned trials: %d/%d.\n\n", len(report.Results), report.PlannedTrials)
		if missing := report.PlannedTrials - len(report.Results); missing > 0 {
			fmt.Fprintf(&b, "**Incomplete run: missing %d of %d planned results.** Scores below cover completed trials only.\n\n", missing, report.PlannedTrials)
		}
	} else {
		fmt.Fprintf(&b, "Completed trials: %d. Planned trial count: unspecified.\n\n", len(report.Results))
	}
	var harness Score
	var scorable, outcome, clean, recovered, errors, modelCalls, toolCalls, rejected, unknown, outputErrors int
	var schemaTrials, schemaScorable, schemaUnattempted, schemaExcluded, firstTool, firstArguments, schemaAttempts, schemaInvalid int
	var duration time.Duration
	var input, output int64
	var inputKnown, outputKnown int
	classes := map[string]int{}
	for _, r := range report.Results {
		harness.Passed += r.Harness.Passed
		harness.Total += r.Harness.Total
		if r.Behavior.Scorable {
			scorable++
			if r.Behavior.OutcomeCorrect {
				outcome++
			}
			if r.Behavior.CleanSuccess {
				clean++
			}
			if r.Behavior.RecoverySuccess {
				recovered++
			}
		}
		if s := r.Schema; s != nil {
			schemaTrials++
			schemaAttempts += s.Attempts
			schemaInvalid += s.InvalidCalls
			switch {
			case !r.Behavior.Scorable:
				schemaExcluded++
			case s.Attempts == 0:
				schemaUnattempted++
			default:
				schemaScorable++
				if s.FirstToolCorrect {
					firstTool++
				}
				if s.FirstArgumentsValid {
					firstArguments++
				}
			}
		}
		if r.Error != "" || r.ErrorClass != "" {
			errors++
			class := r.ErrorClass
			if class == "" {
				class = "unclassified"
			}
			classes[class]++
		}
		modelCalls += r.ModelCalls
		toolCalls += r.ToolCalls
		rejected += r.Behavior.RejectedCalls
		unknown += r.Behavior.UnknownToolErrors
		outputErrors += r.Behavior.OutputErrors
		duration += r.Duration
		if r.InputTokens != nil {
			input += *r.InputTokens
			inputKnown++
		}
		if r.OutputTokens != nil {
			output += *r.OutputTokens
			outputKnown++
		}
	}
	fmt.Fprintf(&b, "## Harness correctness\n\nAssertions passed/exercised: %d/%d. Unexercised contracts are not counted as passes.\n\n", harness.Passed, harness.Total)
	if report.Mode == Scripted {
		b.WriteString("## Behavior: scripted diagnostics\n\nThese are fixed-script diagnostics, not live model performance. Expected negative probes can recover without being clean successes.\n\n")
	} else {
		b.WriteString("## Behavior: live model rates\n\nObserved counts describe this run only; harness assertions are scored separately.\n\n")
	}
	fmt.Fprintf(&b, "Scorable trials: %d/%d; excluded: %d.\n\n", scorable, len(report.Results), len(report.Results)-scorable)
	b.WriteString("| Measure | Successful/scorable trials |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Correct outcome | %d/%d |\n| Clean success | %d/%d |\n| Recovered success | %d/%d |\n\n", outcome, scorable, clean, scorable, recovered, scorable)
	if scorable == 0 {
		b.WriteString("No behavior rate is available because no trials were scorable.\n\n")
	}
	if schemaTrials > 0 {
		b.WriteString("## Schema behavior\n\n")
		fmt.Fprintf(&b, "Schema trials: %d. First decisions scored: %d/%d; no operation attempted: %d; excluded: %d.\n\n", schemaTrials, schemaScorable, schemaTrials, schemaUnattempted, schemaExcluded)
		b.WriteString("The first non-read-only operation is scored separately for tool choice and argument validity against that selected tool's advertised contract. Later recovery does not replace the first score.\n\n")
		b.WriteString("| Measure | Successful/scorable first decisions |\n| --- | ---: |\n")
		fmt.Fprintf(&b, "| Correct first tool | %d/%d |\n| Valid first arguments | %d/%d |\n\n", firstTool, schemaScorable, firstArguments, schemaScorable)
		fmt.Fprintf(&b, "Recorded invalid contract calls: %d/%d attempted operations across all schema trials. Unknown tools count as invalid.\n\n", schemaInvalid, schemaAttempts)
		if schemaScorable == 0 {
			b.WriteString("No first-decision rate is available because no schema trials had a scorable attempted operation.\n\n")
		}
	}
	b.WriteString("## Errors and usage\n\n")
	fmt.Fprintf(&b, "Trials with errors: %d/%d completed trials.\n\n", errors, len(report.Results))
	if len(classes) > 0 {
		keys := make([]string, 0, len(classes))
		for class := range classes {
			keys = append(keys, class)
		}
		sort.Strings(keys)
		b.WriteString("| Error class | Affected/all trials |\n| --- | ---: |\n")
		for _, class := range keys {
			fmt.Fprintf(&b, "| %s | %d/%d |\n", markdownText(class), classes[class], len(report.Results))
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Rejected tool calls: %d/%d tool calls. Unknown tool errors: %d/%d tool calls.\n\n", rejected, toolCalls, unknown, toolCalls)
	fmt.Fprintf(&b, "Output errors: %d/%d model calls.\n\n", outputErrors, modelCalls)
	fmt.Fprintf(&b, "Model calls: %d. Tool calls: %d. Summed trial duration: %s.\n\n", modelCalls, toolCalls, duration)
	fmt.Fprintf(&b, "Input tokens: %s. Output tokens: %s.\n\n", tokenUsage(input, inputKnown, len(report.Results)), tokenUsage(output, outputKnown, len(report.Results)))
	b.WriteString("## Trial details\n\n")
	if len(report.Results) == 0 {
		b.WriteString("No trial results.\n")
	}
	for _, r := range report.Results {
		fmt.Fprintf(&b, "### %s / trial %d\n\n", markdownText(r.ScenarioID), r.Trial)
		fmt.Fprintf(&b, "Outcome: %s. Harness: %d/%d.", markdownText(r.Outcome), r.Harness.Passed, r.Harness.Total)
		if r.Behavior.Scorable {
			fmt.Fprintf(&b, " Behavior: correct=%t, clean=%t, recovered=%t.\n\n", r.Behavior.OutcomeCorrect, r.Behavior.CleanSuccess, r.Behavior.RecoverySuccess)
		} else {
			b.WriteString(" Behavior: unscorable.\n\n")
		}
		if s := r.Schema; s != nil {
			switch {
			case !r.Behavior.Scorable:
				b.WriteString("Schema first decision: excluded (unscorable trial). ")
			case s.Attempts == 0:
				b.WriteString("Schema first decision: no operation attempted. ")
			default:
				fmt.Fprintf(&b, "Schema first decision: correct tool=%t, valid arguments=%t. ", s.FirstToolCorrect, s.FirstArgumentsValid)
			}
			fmt.Fprintf(&b, "Invalid contract calls: %d/%d attempted operations.\n\n", s.InvalidCalls, s.Attempts)
		}
		if r.Trace != "" {
			fmt.Fprintf(&b, "[Trace](%s)", reportLink(dir, r.Trace))
			if r.Manifest != "" {
				fmt.Fprintf(&b, " · [Manifest](%s)", reportLink(dir, r.Manifest))
			}
			fmt.Fprintf(&b, ". Cursors: %s:%d → %s:%d.\n\n", markdownText(r.Start.Session), r.Start.Sequence, markdownText(r.Through.Session), r.Through.Sequence)
		}
		if r.Error != "" || r.ErrorClass != "" {
			fmt.Fprintf(&b, "Error (%s): %s\n\n", markdownText(r.ErrorClass), markdownText(r.Error))
		}
		fmt.Fprintf(&b, "Model calls: %d. Tool calls: %d. Duration: %s.", r.ModelCalls, r.ToolCalls, r.Duration)
		if r.StopReason != "" {
			fmt.Fprintf(&b, " Stop reason: %s.", markdownText(r.StopReason))
		}
		b.WriteString("\n\n")
		failed := 0
		for _, assertion := range r.Assertions {
			if assertion.Passed {
				continue
			}
			failed++
			fmt.Fprintf(&b, "- %s [%s], cursor %s:%d", markdownText(assertion.ID), markdownText(assertion.Track), markdownText(assertion.Cursor.Session), assertion.Cursor.Sequence)
			if assertion.Invocation != "" {
				fmt.Fprintf(&b, ", invocation %s", markdownText(assertion.Invocation))
			}
			fmt.Fprintf(&b, ": expected %s; actual %s.\n", assertionValue(assertion.Expected), assertionValue(assertion.Actual))
		}
		if failed > 0 {
			b.WriteByte('\n')
		} else {
			b.WriteString("Failed assertions: none.\n\n")
		}
	}
	return b.String()
}

func tokenUsage(total int64, known, trials int) string {
	if known == 0 {
		return fmt.Sprintf("unknown (0/%d trials reported)", trials)
	}
	return fmt.Sprintf("%d known (%d/%d trials reported; %d unknown)", total, known, trials, trials-known)
}

func reportLink(dir, path string) string {
	if filepath.IsAbs(path) {
		if base, err := filepath.Abs(dir); err == nil {
			if relative, err := filepath.Rel(base, path); err == nil {
				path = relative
			}
		}
	}
	return (&url.URL{Path: filepath.ToSlash(path)}).String()
}

func assertionValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "(unavailable)"
	}
	return markdownText(string(data))
}

func markdownText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "|", "\\|", "\r", " ", "\n", " ").Replace(s)
}
