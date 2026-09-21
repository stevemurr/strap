// Package evalweb serves eval results over HTTP for reading, comparing and
// analysing runs in a browser. It reads the same files the CLI writes and
// derives everything else, so a run without report.json is still complete.
package evalweb

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/eval/interaction"
)

// Kind tells the two result families apart. They share file names but not
// schemas: a ladder run records tasks and a model, an interaction run records
// a mode and planned trials.
type Kind string

const (
	Ladder      Kind = "ladder"
	Interaction Kind = "interaction"
)

// RunSummary is what the run list needs: identity, provenance and outcome
// counts read from results.jsonl alone, without opening any trace.
type RunSummary struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Group     string    `json:"group,omitempty"`
	Kind      Kind      `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	Commit    string    `json:"commit,omitempty"`
	Profile   string    `json:"profile,omitempty"`
	Model     string    `json:"model,omitempty"`
	Backend   string    `json:"backend,omitempty"`
	HasReport bool      `json:"has_report"`
	Error     string    `json:"error,omitempty"`
	Batch     bool      `json:"batch,omitempty"`
	Members   int       `json:"members,omitempty"`

	Tasks       int                  `json:"tasks"`
	Passed      int                  `json:"passed"`
	Failed      int                  `json:"failed"`
	BuildFailed int                  `json:"build_failed"`
	Errored     int                  `json:"errored"`
	Submitted   int                  `json:"submitted"`
	TimedOut    int                  `json:"timed_out"`
	NoReply     int                  `json:"no_reply"`
	Tiers       map[string]TierCount `json:"tiers,omitempty"`

	Trials *TrialCounts `json:"trials,omitempty"`
}

// TierCount is the pass tally of one tier.
type TierCount struct {
	Tasks  int `json:"tasks"`
	Passed int `json:"passed"`
}

// TrialCounts summarises an interaction run.
type TrialCounts struct {
	Mode      string `json:"mode"`
	Planned   int    `json:"planned"`
	Completed int    `json:"completed"`
	Passed    int    `json:"passed"`
	Clean     int    `json:"clean"`
	Recovered int    `json:"recovered"`
	Scorable  int    `json:"scorable"`
}

var skippedDirs = map[string]bool{"workspace": true, ".git": true, "ladder": true, "grading": true, "problems": true, "__pycache__": true, "final-source": true, "outbox": true, "config": true, "node_modules": true}

// maxDepth bounds discovery below the root. Container batches nest a run
// three directories down (batch/task/results), which is the deepest layout.
const maxDepth = 4

// discover walks root for directories holding results.jsonl and summarises
// each. Runs are returned newest first.
func discover(root string) ([]RunSummary, error) {
	var runs []RunSummary
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if p != root && (skippedDirs[d.Name()] || strings.Count(rel, string(filepath.Separator)) >= maxDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "results.jsonl" {
			return nil
		}
		dir := filepath.Dir(p)
		relDir, _ := filepath.Rel(root, dir)
		runs = append(runs, summarize(dir, filepath.ToSlash(relDir)))
		return nil
	})
	if err != nil {
		return nil, err
	}
	runs = addBatches(root, runs)
	sort.SliceStable(runs, func(i, j int) bool {
		if !runs[i].StartedAt.Equal(runs[j].StartedAt) {
			return runs[i].StartedAt.After(runs[j].StartedAt)
		}
		return runs[i].Path < runs[j].Path
	})
	return runs, nil
}

func summarize(dir, rel string) RunSummary {
	s := RunSummary{Path: rel, Name: path.Base(rel), Kind: Ladder}
	if parent := path.Dir(rel); parent != "." {
		s.Group = strings.SplitN(rel, "/", 2)[0]
		// Container batches nest each task as <task>/results; name them by task.
		if s.Name == "results" {
			s.Name = path.Base(parent)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err == nil {
		s.HasReport = true
	}
	var run struct {
		eval.RunInfo
		Mode string `json:"mode"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "run.json")); err == nil {
		_ = json.Unmarshal(data, &run)
	}
	s.StartedAt, s.Commit, s.Profile = run.StartedAt, run.Commit, run.Profile
	if run.Mode != "" {
		s.Kind = Interaction
		summarizeInteraction(dir, &s)
		return s
	}
	s.Model, s.Backend = run.Model.Model, run.Model.Backend
	results, err := latestResults(dir)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Tiers = map[string]TierCount{}
	var earliest time.Time
	for _, r := range results {
		s.Tasks++
		tier := s.Tiers[r.Tier]
		tier.Tasks++
		if r.Passed {
			tier.Passed++
			s.Passed++
		}
		s.Tiers[r.Tier] = tier
		switch r.Outcome {
		case eval.Failed:
			s.Failed++
		case eval.BuildFailed:
			s.BuildFailed++
		case eval.Errored:
			s.Errored++
		case eval.Submitted:
			s.Submitted++
		}
		if r.TimedOut {
			s.TimedOut++
		}
		if r.NoReply {
			s.NoReply++
		}
		if earliest.IsZero() || r.StartedAt.Before(earliest) {
			earliest = r.StartedAt
		}
	}
	if s.StartedAt.IsZero() {
		s.StartedAt = earliest
	}
	return s
}

func summarizeInteraction(dir string, s *RunSummary) {
	report, err := interaction.ReadReport(dir)
	if err != nil {
		s.Error = err.Error()
		return
	}
	t := &TrialCounts{Mode: string(report.Mode), Planned: report.PlannedTrials, Completed: len(report.Results)}
	var earliest time.Time
	for _, r := range report.Results {
		if r.Outcome == "passed" {
			t.Passed++
		}
		if r.Behavior.Scorable {
			t.Scorable++
		}
		if r.Behavior.CleanSuccess {
			t.Clean++
		}
		if r.Behavior.RecoverySuccess {
			t.Recovered++
		}
		if earliest.IsZero() || r.StartedAt.Before(earliest) {
			earliest = r.StartedAt
		}
	}
	s.Trials = t
	s.Tasks = len(report.Results)
	s.Passed = t.Passed
	if s.StartedAt.IsZero() {
		s.StartedAt = earliest
	}
}

// latestResults reads results.jsonl keeping the last record per task, the
// same rule eval.Analyze applies, in tier then id order.
func latestResults(dir string) ([]eval.Result, error) {
	f, err := os.Open(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	latest := map[string]eval.Result{}
	var order []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var r eval.Result
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, errors.New("results.jsonl: " + err.Error())
		}
		if _, seen := latest[r.TaskID]; !seen {
			order = append(order, r.TaskID)
		}
		latest[r.TaskID] = r
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	tiers := map[string]int{}
	for i, tier := range eval.Tiers() {
		tiers[tier] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := latest[order[i]], latest[order[j]]
		if tiers[a.Tier] != tiers[b.Tier] {
			return tiers[a.Tier] < tiers[b.Tier]
		}
		return a.TaskID < b.TaskID
	})
	out := make([]eval.Result, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out, nil
}
