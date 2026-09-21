package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Submission is the versioned handoff between the agent and grader containers.
// Only /outbox/submission is ready; .pending is never consumed by the grader.
type Submission struct {
	Version int     `json:"version"`
	Run     RunInfo `json:"run"`
	Result  Result  `json:"result"`
}

func publishSubmission(ctx context.Context, mounts Mounts, run RunInfo, result Result) error {
	pending := filepath.Join(mounts.Outbox, ".pending")
	if err := os.Mkdir(pending, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(pending)
	if err := copyTree(mounts.Workspace, filepath.Join(pending, "workspace")); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(pending, "manifest.json"), Submission{Version: 1, Run: run, Result: result}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(pending, filepath.Join(mounts.Outbox, "submission"))
}

func saveResult(dir string, r Result) error {
	if err := writeJSON(filepath.Join(dir, r.TaskID, "result.json"), r); err != nil {
		return err
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "results.jsonl"), append(line, '\n'), 0o644)
}

// GradeSubmission consumes one published outbox snapshot. Mount Outbox read-only,
// Grading with private task fixtures, Results with the agent's recorded artifacts,
// and Workspace as an empty directory in a fresh grading container.
// Neither the submitted snapshot nor the original agent workspace is modified.
func GradeSubmission(ctx context.Context, mounts Mounts) (Result, error) {
	mounts, err := mounts.resolve(true)
	if err != nil {
		return Result{}, err
	}
	if err := requireEmpty(mounts.Workspace); err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(filepath.Join(mounts.Outbox, "submission", "manifest.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read ready submission: %w", err)
	}
	var submission Submission
	if err := json.Unmarshal(data, &submission); err != nil {
		return Result{}, err
	}
	if submission.Version != 1 {
		return Result{}, fmt.Errorf("unsupported submission version %d", submission.Version)
	}
	r := submission.Result
	if r.Outcome != Submitted || r.Session == "" || len(submission.Run.Tasks) != 1 || submission.Run.Tasks[0] != r.TaskID {
		return Result{}, errors.New("invalid submission identity or outcome")
	}
	tasks, err := LoadLadder(mounts.Grading)
	if err != nil {
		return Result{}, err
	}
	var task Task
	for _, candidate := range tasks {
		if candidate.ID == r.TaskID {
			task = candidate
			break
		}
	}
	if task.ID == "" {
		return Result{}, fmt.Errorf("unknown submitted problem %q", r.TaskID)
	}
	// Results must belong to this submission. Grading may be repeated using a
	// fresh grading workspace, but must never overwrite another attempt's data.
	data, err = os.ReadFile(filepath.Join(mounts.Results, task.ID, "result.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read agent result: %w", err)
	}
	var recorded Result
	if err := json.Unmarshal(data, &recorded); err != nil {
		return Result{}, err
	}
	if recorded.Session != r.Session || recorded.TaskID != r.TaskID {
		return Result{}, errors.New("results mount does not match submission")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	gradeErr := copyTree(filepath.Join(mounts.Outbox, "submission", "workspace"), mounts.Workspace)
	if gradeErr == nil {
		gradeErr = ApplyHidden(task, mounts.Workspace)
	}
	if gradeErr == nil {
		grade, err := RunHiddenTests(ctx, task, mounts.Workspace)
		gradeErr = err
		if err == nil {
			r.Grade = &grade
			switch {
			case grade.Passed:
				r.Outcome = Passed
			case !grade.Compiled:
				r.Outcome = BuildFailed
			default:
				r.Outcome = Failed
			}
		}
	}
	if gradeErr != nil {
		r.Outcome, r.Error = Errored, gradeErr.Error()
	}
	r.Passed = r.Outcome == Passed
	return r, errors.Join(gradeErr, saveResult(mounts.Results, r))
}
