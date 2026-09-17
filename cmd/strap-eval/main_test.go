package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevemurr/strap/eval"
)

func TestTierSelection(t *testing.T) {
	for _, tc := range []struct {
		tiers string
		count int
	}{{"", 60}, {"easy", 20}, {" easy, hard,easy ", 40}, {"hard,medium,easy", 60}} {
		s := selection{ladder: "../../eval/ladder", tier: tc.tiers}
		tasks, err := s.load()
		if err != nil || len(tasks) != tc.count {
			t.Fatalf("%q: %d tasks, %v", tc.tiers, len(tasks), err)
		}
		if tc.count == 60 && tasks[0].Tier != "easy" {
			t.Fatal("tier order changed")
		}
	}
	s := selection{ladder: "../../eval/ladder", tier: "easy,medium", tasks: "easy-01-budget-pair,hard-01-rolling-peak"}
	tasks, err := s.load()
	if err != nil || len(tasks) != 1 || tasks[0].ID != "easy-01-budget-pair" {
		t.Fatal(tasks, err)
	}
	for _, tier := range []string{"eazy", "easy,", "easy,,hard", ",", " "} {
		for _, command := range []string{"run", "list", "selfcheck"} {
			err := run(context.Background(), []string{command, "-tier", tier}, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "invalid tier") {
				t.Fatalf("%s %q: %v", command, tier, err)
			}
		}
	}
}

func TestNonTerminalDisplay(t *testing.T) {
	for _, mode := range []string{"auto", "plain", "quiet"} {
		enabled, err := useTUI(mode, strings.NewReader(""), new(bytes.Buffer))
		if enabled || err != nil {
			t.Fatal(mode, enabled, err)
		}
	}
	for _, mode := range []string{"tui", "invalid"} {
		if _, err := useTUI(mode, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("accepted", mode)
		}
	}
}

func TestRunAutomaticallyReportsReusedResults(t *testing.T) {
	for _, display := range []struct {
		name  string
		args  []string
		quiet bool
	}{
		{"plain", []string{"-ui", "plain"}, false},
		{"quiet", []string{"-ui", "quiet"}, true},
		{"short", []string{"-q"}, true},
		{"override_tui", []string{"-ui", "tui", "-q"}, true},
		{"override_tui_reverse", []string{"-q", "-ui", "tui"}, true},
		{"disabled", []string{"-q=false", "-ui", "plain"}, false},
		{"completion_delay", []string{"-q", "-quiet", "10ms"}, true},
	} {
		t.Run(display.name, func(t *testing.T) {
			for _, report := range []bool{true, false} {
				t.Run(map[bool]string{true: "report", false: "disabled"}[report], func(t *testing.T) {
					dir := t.TempDir()
					cfg := filepath.Join(dir, "models.json")
					if err := os.WriteFile(cfg, []byte(`{"default":"test","models":{"test":{"model":"test","base_url":"http://127.0.0.1:1"}}}`), 0600); err != nil {
						t.Fatal(err)
					}
					r := eval.Result{TaskID: "easy-01-budget-pair", Tier: "easy", Title: "Budget pair", Outcome: eval.Passed, Passed: true}
					data, _ := json.Marshal(r)
					if err := os.MkdirAll(filepath.Join(dir, r.TaskID), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, r.TaskID, "result.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "results.jsonl"), append(data, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
					args := []string{"run", "-config", cfg, "-ladder", "../../eval/ladder", "-tier", "easy,medium", "-task", r.TaskID, "-out", dir}
					args = append(args, display.args...)
					if !report {
						args = append(args, "-report=false")
					}
					var out, logs bytes.Buffer
					if err := run(context.Background(), args, &out, &logs); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(out.String(), "1/1 passed") || strings.Contains(out.String(), "\x1b[") {
						t.Fatal(out.String())
					}
					if display.quiet && logs.Len() != 0 {
						t.Fatalf("quiet run emitted progress logs: %s", logs.String())
					}
					if !display.quiet && !strings.Contains(logs.String(), "reusing") {
						t.Fatalf("plain run omitted progress logs: %s", logs.String())
					}
					if strings.Contains(out.String(), "reports:") != report {
						t.Fatalf("unexpected report summary: %s", out.String())
					}
					for _, file := range []string{"report.md", "report.json"} {
						_, err := os.Stat(filepath.Join(dir, file))
						if report && err != nil {
							t.Fatal(err)
						}
						if !report && !os.IsNotExist(err) {
							t.Fatal("report disabled but file exists", err)
						}
					}
				})
			}
		})
	}
}

func TestQuietRunReturnsErrors(t *testing.T) {
	for _, args := range [][]string{{"-q"}, {"-ui", "quiet"}} {
		var out, stderr bytes.Buffer
		args = append([]string{"run"}, args...)
		args = append(args, "-config", filepath.Join(t.TempDir(), "missing.json"))
		err := run(context.Background(), args, &out, &stderr)
		if err == nil || !strings.Contains(err.Error(), "missing.json") {
			t.Fatalf("quiet run lost configuration error: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("failed run emitted a success summary: %s", out.String())
		}
	}
}
