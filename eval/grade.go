package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Grade is the outcome of running the hidden tests in a workspace.
type Grade struct {
	Passed   bool          `json:"passed"`
	Compiled bool          `json:"compiled"`
	Command  string        `json:"command"`
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration_ns"`
}

const gradeOutputLimit = 32 << 10

// RunHiddenTests runs only the TestHidden* functions in the workspace. The
// agent's own tests still compile, so a broken test file fails the build the
// same way it would for a person running go test. It passes when at least one
// hidden test ran and none failed: counting the tests, not matching text, lets
// a workspace hold other packages without tests (an agent's scratch program
// once failed seven correct solutions by printing "[no test files]"). An error
// is returned only when the Go toolchain could not be run at all.
func RunHiddenTests(ctx context.Context, task Task, dir string) (Grade, error) {
	args := []string{"test", "./...", "-json", "-count=1", "-run", "^TestHidden", "-timeout", task.GradeTimeout().String()}
	g := Grade{Command: "go " + strings.Join(args, " ")}
	ctx, cancel := context.WithTimeout(ctx, task.GradeTimeout()+time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "CGO_ENABLED=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	started := time.Now()
	err := cmd.Run()
	g.Duration = time.Since(started)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		g.Output = bound(out.String(), gradeOutputLimit)
		return g, fmt.Errorf("run go test: %w", err)
	}
	text, passed, failed := readTestJSON(out.String())
	g.Output = bound(text, gradeOutputLimit)
	g.Compiled = !strings.Contains(text, "[build failed]") && !strings.Contains(text, "[setup failed]") && !strings.Contains(text, "cannot find package") && !strings.Contains(text, "no Go files")
	g.Passed = err == nil && passed > 0 && failed == 0
	return g, nil
}

// readTestJSON turns go test -json output back into the usual text and counts
// top-level hidden tests that passed and failed. Lines that are not events,
// such as toolchain errors, are kept as text.
func readTestJSON(raw string) (text string, passed, failed int) {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		var e struct{ Action, Test, Output string }
		if json.Unmarshal([]byte(line), &e) != nil {
			b.WriteString(line + "\n")
			continue
		}
		switch e.Action {
		case "output", "build-output":
			b.WriteString(e.Output)
		case "pass", "fail":
			if strings.HasPrefix(e.Test, "TestHidden") && !strings.Contains(e.Test, "/") {
				if e.Action == "pass" {
					passed++
				} else {
					failed++
				}
			}
		}
	}
	return b.String(), passed, failed
}

func bound(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	head := limit / 4
	tail := limit - head
	return s[:head] + "\n…[truncated]…\n" + s[len(s)-tail:]
}
