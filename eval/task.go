// Package eval runs coding tasks in fixed container workspaces, publishes
// submissions to an outbox, and grades them in a separate process.
//
// A ladder is a directory tree: <ladder>/<tier>/<task>/ holding task.json, a
// workspace/ module that the agent sees, hidden/ test files copied in after the
// session ends, and reference/ files that replace the stubs when the ladder
// checks itself.
package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Task is one ladder entry. Prompt is the exact user message sent to the root
// agent; the workspace README carries the full contract so the agent must read
// the repository the way a person would.
type Task struct {
	ID          string `json:"id"`
	Tier        string `json:"tier"`
	Title       string `json:"title"`
	Insight     string `json:"insight"`
	Prompt      string `json:"prompt"`
	Timeout     string `json:"timeout,omitempty"`      // Session budget, default per tier.
	TestTimeout string `json:"test_timeout,omitempty"` // Hidden test binary budget, default 3m.
	Dir         string `json:"-"`
}

var tierOrder = map[string]int{"easy": 0, "medium": 1, "hard": 2}

var defaultTimeouts = map[string]time.Duration{"easy": 15 * time.Minute, "medium": 25 * time.Minute, "hard": 40 * time.Minute}

const defaultTestTimeout = 3 * time.Minute

// Tiers lists the ladder tiers from easiest to hardest.
func Tiers() []string { return []string{"easy", "medium", "hard"} }

func (t Task) SessionTimeout() time.Duration {
	if d, err := time.ParseDuration(t.Timeout); err == nil && d > 0 {
		return d
	}
	return defaultTimeouts[t.Tier]
}

func (t Task) GradeTimeout() time.Duration {
	if d, err := time.ParseDuration(t.TestTimeout); err == nil && d > 0 {
		return d
	}
	return defaultTestTimeout
}

func (t Task) WorkspaceDir() string { return filepath.Join(t.Dir, "workspace") }
func (t Task) HiddenDir() string    { return filepath.Join(t.Dir, "hidden") }
func (t Task) ReferenceDir() string { return filepath.Join(t.Dir, "reference") }

// Validate checks the task's files without running anything.
func (t Task) validatePublic() error {
	var errs []error
	if t.ID == "" || t.ID == "." || t.ID == ".." || strings.ContainsAny(t.ID, "/\\") {
		errs = append(errs, errors.New("id must be a nonempty single path component"))
	}
	if _, ok := tierOrder[t.Tier]; !ok {
		errs = append(errs, fmt.Errorf("tier %q must be easy, medium or hard", t.Tier))
	}
	if strings.TrimSpace(t.Prompt) == "" {
		errs = append(errs, errors.New("prompt is required"))
	}
	if t.Timeout != "" {
		if _, err := time.ParseDuration(t.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("timeout: %w", err))
		}
	}
	if t.TestTimeout != "" {
		if _, err := time.ParseDuration(t.TestTimeout); err != nil {
			errs = append(errs, fmt.Errorf("test_timeout: %w", err))
		}
	}
	if _, err := os.Stat(filepath.Join(t.WorkspaceDir(), "go.mod")); err != nil {
		errs = append(errs, errors.New("workspace/go.mod is required"))
	}
	return errors.Join(errs...)
}

// Validate includes private grading assets; the agent phase only validates public files.
func (t Task) Validate() error {
	var errs []error
	if err := t.validatePublic(); err != nil {
		errs = append(errs, err)
	}
	hidden, err := goFiles(t.HiddenDir())
	if err != nil {
		errs = append(errs, fmt.Errorf("hidden: %w", err))
	}
	tests := 0
	for _, name := range hidden {
		if !strings.HasSuffix(name, "_test.go") {
			errs = append(errs, fmt.Errorf("hidden/%s must be a _test.go file", name))
			continue
		}
		tests++
		body, err := os.ReadFile(filepath.Join(t.HiddenDir(), name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !strings.Contains(string(body), "func TestHidden") {
			errs = append(errs, fmt.Errorf("hidden/%s must define TestHidden* functions; grading runs -run ^TestHidden", name))
		}
	}
	if tests == 0 {
		errs = append(errs, errors.New("hidden/ needs at least one _test.go file"))
	}
	reference, err := goFiles(t.ReferenceDir())
	if err != nil {
		errs = append(errs, fmt.Errorf("reference: %w", err))
	}
	if len(reference) == 0 {
		errs = append(errs, errors.New("reference/ needs at least one .go file"))
	}
	for _, name := range reference {
		if _, err := os.Stat(filepath.Join(t.WorkspaceDir(), name)); err != nil {
			errs = append(errs, fmt.Errorf("reference/%s must replace a workspace file of the same name", name))
		}
	}
	return errors.Join(errs...)
}

func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func loadTask(dir string, grading bool) (Task, error) {
	data, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		return Task{}, err
	}
	var t Task
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&t); err != nil {
		return Task{}, fmt.Errorf("%s: %w", filepath.Join(dir, "task.json"), err)
	}
	t.Dir = dir
	validate := t.validatePublic
	if grading {
		validate = t.Validate
	}
	if err := validate(); err != nil {
		return Task{}, fmt.Errorf("%s: %w", dir, err)
	}
	return t, nil
}

// LoadLadder reads every task under <dir>/<tier>/<name>/task.json and returns
// them ordered by tier, then directory name.
func LoadLadder(dir string) ([]Task, error) { return loadLadder(dir, true) }

// LoadProblems reads only task metadata and starter files, without accessing hidden tests.
func LoadProblems(dir string) ([]Task, error) { return loadLadder(dir, false) }

func loadLadder(dir string, grading bool) ([]Task, error) {
	var tasks []Task
	seen := map[string]string{}
	var errs []error
	for _, tier := range Tiers() {
		entries, err := os.ReadDir(filepath.Join(dir, tier))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			taskDir := filepath.Join(dir, tier, e.Name())
			if _, err := os.Stat(filepath.Join(taskDir, "task.json")); err != nil {
				continue
			}
			t, err := loadTask(taskDir, grading)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if t.Tier != tier {
				errs = append(errs, fmt.Errorf("%s: tier %q does not match directory %s", taskDir, t.Tier, tier))
				continue
			}
			if prev, dup := seen[t.ID]; dup {
				errs = append(errs, fmt.Errorf("%s: duplicate id %q also in %s", taskDir, t.ID, prev))
				continue
			}
			seen[t.ID] = taskDir
			tasks = append(tasks, t)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks under %s", dir)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tierOrder[tasks[i].Tier] != tierOrder[tasks[j].Tier] {
			return tierOrder[tasks[i].Tier] < tierOrder[tasks[j].Tier]
		}
		return tasks[i].Dir < tasks[j].Dir
	})
	return tasks, nil
}

func findTask(tasks []Task, id string) (Task, bool) {
	for _, t := range tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}
