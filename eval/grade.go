package eval

import (
	"bytes"
	"context"
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
// same way it would for a person running go test. An error is returned only
// when the Go toolchain could not be run at all.
func RunHiddenTests(ctx context.Context, task Task, dir string) (Grade, error) {
	args := []string{"test", "./...", "-count=1", "-run", "^TestHidden", "-timeout", task.GradeTimeout().String()}
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
	g.Output = bound(out.String(), gradeOutputLimit)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return g, fmt.Errorf("run go test: %w", err)
	}
	text := out.String()
	g.Compiled = !strings.Contains(text, "[build failed]") && !strings.Contains(text, "[setup failed]") && !strings.Contains(text, "cannot find package") && !strings.Contains(text, "no Go files")
	g.Passed = err == nil && !strings.Contains(text, "no test files") && !strings.Contains(text, "no tests to run")
	return g, nil
}

func bound(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	head := limit / 4
	tail := limit - head
	return s[:head] + "\n…[truncated]…\n" + s[len(s)-tail:]
}
