package evalweb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/internal/evalwire"
)

func (r *Runner) commandContext(ctx context.Context, args ...string) *exec.Cmd {
	if r.command != nil {
		return r.command(ctx, args...)
	}
	return exec.CommandContext(ctx, "container", args...)
}

func (r *Runner) image() string {
	if r.Image != "" {
		return r.Image
	}
	return "strap-eval"
}

type containerInputs struct{ config, problems, grading string }

// Snapshot one batch's inputs. Only public problem fixtures and the resolved
// config are mounted into agents; the private ladder is grader-only.
func (r *Runner) prepareContainers(ctx context.Context, dir string, tasks []eval.Task) (containerInputs, error) {
	inputs := containerInputs{config: filepath.Join(dir, "config"), problems: filepath.Join(dir, "problems"), grading: filepath.Join(dir, "grading")}
	if err := os.MkdirAll(inputs.config, 0o700); err != nil {
		return inputs, err
	}
	if err := os.MkdirAll(inputs.problems, 0o755); err != nil {
		return inputs, err
	}
	if err := os.CopyFS(inputs.grading, os.DirFS(r.Ladder)); err != nil {
		return inputs, fmt.Errorf("snapshot grading fixtures: %w", err)
	}
	for _, task := range tasks {
		rel, err := filepath.Rel(r.Ladder, task.Dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return inputs, fmt.Errorf("problem outside ladder: %s", task.ID)
		}
		source, target := filepath.Join(inputs.grading, rel), filepath.Join(inputs.problems, rel)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return inputs, err
		}
		metadata, err := os.ReadFile(filepath.Join(source, "task.json"))
		if err != nil {
			return inputs, err
		}
		if err := os.WriteFile(filepath.Join(target, "task.json"), metadata, 0o644); err != nil {
			return inputs, err
		}
		if err := os.CopyFS(filepath.Join(target, "workspace"), os.DirFS(filepath.Join(source, "workspace"))); err != nil {
			return inputs, err
		}
	}
	snapshot, err := json.Marshal(evalwire.Config{Version: evalwire.Version, Harness: r.Config, Profile: r.Profile})
	if err != nil {
		return inputs, err
	}
	if err := os.WriteFile(filepath.Join(inputs.config, "run.json"), snapshot, 0o600); err != nil {
		return inputs, err
	}
	if !r.NoBuild {
		source := r.BuildContext
		if source == "" {
			source = filepath.Dir(filepath.Dir(r.Ladder))
		}
		source, err = filepath.Abs(source)
		if err != nil {
			return inputs, err
		}
		recipe := filepath.Join(source, "eval", "Dockerfile")
		if _, err := os.Stat(recipe); err != nil {
			return inputs, fmt.Errorf("eval image recipe: %w (set -build-context or use -no-build with a compatible image)", err)
		}
		log, err := os.Create(filepath.Join(dir, "build.log"))
		if err != nil {
			return inputs, err
		}
		defer log.Close()
		cmd := r.commandContext(ctx, "build", "-f", recipe, "-t", r.image(), source)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Run(); err != nil {
			return inputs, fmt.Errorf("build eval image (see build.log): %w", err)
		}
	}
	return inputs, nil
}

func bind(source, target string, readonly bool) (string, error) {
	// container's --mount value is comma-delimited, even though exec does not
	// invoke a shell. Reject ambiguous paths rather than mounting the wrong input.
	if strings.ContainsAny(source, ",\r\n") {
		return "", fmt.Errorf("container mount path contains a comma or newline: %q", source)
	}
	mount := "type=bind,source=" + source + ",target=" + target
	if readonly {
		mount += ",readonly"
	}
	return mount, nil
}

func (r *Runner) containerTask(ctx context.Context, j *job, task eval.Task, attempt string, inputs containerInputs) (eval.Result, error) {
	var result eval.Result
	workspace, results, outbox := filepath.Join(attempt, "workspace"), filepath.Join(attempt, "results"), filepath.Join(attempt, "outbox")
	if err := mkdirs(workspace, results, outbox); err != nil {
		return result, err
	}
	makeArgs := func(name string, mounts [][3]string) ([]string, error) {
		args := []string{"run", "--rm", "--progress", "none", "--name", name, "--memory", "2g"}
		for _, m := range mounts {
			value, err := bind(m[0], m[1], m[2] == "ro")
			if err != nil {
				return nil, err
			}
			args = append(args, "--mount", value)
		}
		return args, nil
	}
	// Job id and task id never become shell code. Unique names bound cancellation
	// cleanup to containers owned by this job, not any other running service.
	name := "strap-eval-web-" + j.id + "-" + task.ID
	args, err := makeArgs(name+"-agent", [][3]string{{workspace, "/workspace", ""}, {results, "/results", ""}, {outbox, "/outbox", ""}, {inputs.config, "/config", "ro"}, {inputs.problems, "/problems", "ro"}})
	if err != nil {
		return result, err
	}
	args = append(args, "--cpus", "2", r.image(), "-problem", task.ID, "-q", "-run-config", "/config/run.json", "-progress-json", "-quiet", r.Quiet.String(), "-idle", r.Idle.String())
	j.observe(eval.Progress{Task: task, Phase: eval.Starting, At: time.Now()})
	if err := r.executeContainer(ctx, name+"-agent", args, attempt, "agent", func(line []byte) error {
		var wire evalwire.Progress
		if err := json.Unmarshal(line, &wire); err != nil {
			return fmt.Errorf("invalid agent progress: %w", err)
		}
		p, err := wire.Decode(task)
		if err != nil {
			return err
		}
		j.observe(p)
		return nil
	}); err != nil {
		// Startup and session failures can still leave a useful result. Keep
		// its timings and execution error instead of discarding it on exit 1.
		if saved, readErr := readContainerResult(results, task.ID); readErr == nil {
			result = saved
			if saved.Error != "" {
				err = fmt.Errorf("%w\nagent result: %s", err, saved.Error)
			}
		}
		return result, err
	}
	result, err = readContainerResult(results, task.ID)
	if err != nil {
		return result, err
	}
	if result.Outcome != eval.Submitted {
		return result, fmt.Errorf("agent did not submit: %s %s", result.Outcome, result.Error)
	}
	if _, err := os.Stat(filepath.Join(outbox, "submission", "manifest.json")); err != nil {
		return result, fmt.Errorf("agent exited without ready submission: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	j.observe(eval.Progress{Task: task, Phase: eval.Grading, At: time.Now()})
	args, err = makeArgs(name+"-grader", [][3]string{{outbox, "/outbox", "ro"}, {results, "/results", ""}, {inputs.grading, "/grading", "ro"}})
	if err != nil {
		return result, err
	}
	// No original workspace, model config, or network in the grader. The image
	// supplies its own empty /workspace for the submitted snapshot and hidden tests.
	args = append(args, "--network", "none", r.image(), "grade", "-q")
	if err := r.executeContainer(ctx, name+"-grader", args, attempt, "grader", nil); err != nil {
		return result, err
	}
	result, err = readContainerResult(results, task.ID)
	if err == nil && result.Outcome == eval.Submitted {
		err = errors.New("grader exited without a grade")
	}
	return result, err
}

func readContainerResult(dir, id string) (eval.Result, error) {
	var r eval.Result
	data, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	if r.TaskID != id {
		return r, fmt.Errorf("unexpected task in container result: %q", r.TaskID)
	}
	return r, nil
}

func (r *Runner) executeContainer(ctx context.Context, name string, args []string, dir, phase string, observe func([]byte) error) error {
	log, err := os.Create(filepath.Join(dir, phase+".log"))
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := r.commandContext(ctx, args...)
	cmd.Stderr = log
	// A killed CLI must not leave its eval container running. This only follows
	// explicit job cancellation and only addresses the unique name created here.
	defer func() {
		if ctx.Err() == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = r.commandContext(cleanup, "stop", name).Run()
		_ = r.commandContext(cleanup, "delete", name).Run()
	}()
	if observe == nil {
		cmd.Stdout = log
		if err := cmd.Run(); err != nil {
			return containerFailure(dir, phase, err)
		}
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	progress, err := os.Create(filepath.Join(dir, "progress.jsonl"))
	if err != nil {
		return err
	}
	defer progress.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(io.TeeReader(stdout, progress))
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	var progressErr error
	for scanner.Scan() {
		if err := observe(scanner.Bytes()); err != nil && progressErr == nil {
			progressErr = err
		}
	}
	scanErr := scanner.Err()
	// On a malformed/oversized line drain stdout so Wait cannot deadlock while
	// the child writes. Keep the raw transcript for diagnosis.
	if scanErr != nil {
		_, _ = io.Copy(progress, stdout)
	}
	runErr := cmd.Wait()
	if err := errors.Join(runErr, scanErr, progressErr); err != nil {
		return containerFailure(dir, phase, err)
	}
	return nil
}

// Keep the original exit error for errors.Is/As and surface a bounded stderr
// tail in the page. The full log remains on disk; stdout may contain the
// machine-readable progress protocol and is not substituted for stderr.
func containerFailure(dir, phase string, cause error) error {
	const limit = 4096
	path := filepath.Join(dir, phase+".log")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s container (see %s.log): %w", phase, phase, cause)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%s container (see %s.log): %w", phase, phase, cause)
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	b, _ := io.ReadAll(io.NewSectionReader(f, start, limit))
	tail := strings.TrimSpace(strings.ToValidUTF8(string(b), "�"))
	if tail == "" {
		return fmt.Errorf("%s container (see %s.log and progress.jsonl): %w", phase, phase, cause)
	}
	return fmt.Errorf("%s container: %w\n%s.log (last %d bytes):\n%s", phase, cause, phase, len(b), tail)
}
