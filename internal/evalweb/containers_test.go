package evalweb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/internal/evalwire"
	"github.com/stevemurr/strap/message"
)

// An OS subprocess exercises the real exec, stdout and cancellation paths. It
// records the runtime argv and writes container-shaped artifacts, without a
// container daemon, model request, host shell tool or host grading execution.
func fakeContainer(t *testing.T, mode string) (*Runner, string) {
	t.Helper()
	state := t.TempDir()
	ladder, err := filepath.Abs("../../eval/ladder")
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Config: harness.DefaultConfig(), Ladder: ladder, Image: "test-eval", NoBuild: true, Profile: "test", Quiet: time.Millisecond, Idle: time.Minute}
	r.command = func(ctx context.Context, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestContainerHelperProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "STRAP_CONTAINER_HELPER=1", "STRAP_CONTAINER_STATE="+state, "STRAP_CONTAINER_MODE="+mode)
		return cmd
	}
	return r, state
}

func TestContainerHelperProcess(t *testing.T) {
	if os.Getenv("STRAP_CONTAINER_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	state, mode := os.Getenv("STRAP_CONTAINER_STATE"), os.Getenv("STRAP_CONTAINER_MODE")
	f, err := os.OpenFile(filepath.Join(state, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	_ = json.NewEncoder(f).Encode(args)
	_ = f.Close()
	if args[0] == "stop" || args[0] == "delete" {
		os.Exit(0)
	}
	if args[0] == "build" {
		if mode == "build-fail" {
			os.Exit(9)
		}
		os.Exit(0)
	}
	mounts := map[string]string{}
	for i, a := range args {
		if a == "--mount" {
			var source, target string
			for _, p := range strings.Split(args[i+1], ",") {
				if strings.HasPrefix(p, "source=") {
					source = strings.TrimPrefix(p, "source=")
				}
				if strings.HasPrefix(p, "target=") {
					target = strings.TrimPrefix(p, "target=")
				}
			}
			mounts[target] = source
		}
	}
	grader := args[len(args)-2] == "grade"
	if !grader {
		// Delay makes the overlapping-job refusal deterministic.
		time.Sleep(75 * time.Millisecond)
		if mode == "agent-fail" {
			fmt.Fprintln(os.Stderr, "agent failed")
			os.Exit(7)
		}
		if mode == "cancel" {
			_ = os.WriteFile(filepath.Join(state, "started"), nil, 0600)
			time.Sleep(time.Minute)
			os.Exit(0)
		}
	}
	id := "easy-01-budget-pair"
	for i, a := range args {
		if a == "-problem" {
			id = args[i+1]
		}
	}
	results := mounts["/results"]
	_ = os.MkdirAll(filepath.Join(results, id), 0755)
	r := eval.Result{TaskID: id, Tier: "easy", Title: "Budget pair", Workspace: "/workspace", Trace: "/results/" + id + "/trace.jsonl", Outcome: eval.Submitted, StartedAt: time.Now(), FinishedAt: time.Now()}
	if grader {
		r.Outcome = eval.Failed
		if mode == "grader-fail" {
			os.Exit(8)
		}
	} else {
		// Verify the child receives an unmodified, versioned model config.
		b, err := os.ReadFile(filepath.Join(mounts["/config"], "run.json"))
		if err != nil {
			panic(err)
		}
		if _, err := evalwire.ParseConfig(b); err != nil {
			panic(err)
		}
		for _, private := range []string{"hidden", "reference"} {
			if _, err := os.Stat(filepath.Join(mounts["/problems"], "easy", "01-budget-pair", private)); !os.IsNotExist(err) {
				panic("private fixture exposed to agent")
			}
		}
		if mode != "no-submission" {
			_ = os.MkdirAll(filepath.Join(mounts["/outbox"], "submission"), 0755)
			_ = os.WriteFile(filepath.Join(mounts["/outbox"], "submission", "manifest.json"), []byte(`{}`), 0600)
		}
		trace := filepath.Join(results, id, "trace.jsonl")
		d := eventlog.Data{Kind: "usage", Time: time.Now(), Payload: json.RawMessage(`{"agent":"root","observation":{"usage":{"input_tokens":10,"output_tokens":20}}}`)}
		event := eventlog.Event{Schema: eventlog.SchemaVersion, Session: "fake", Sequence: 1, Data: d}
		b, _ = json.Marshal(event)
		_ = os.WriteFile(trace, append(b, '\n'), 0600)
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(evalwire.Progress{Version: evalwire.Version, Task: id, Phase: eval.Running, At: time.Now()})
		_ = enc.Encode(evalwire.Progress{Version: evalwire.Version, Task: id, At: time.Now(), Event: &event})
		if mode == "bad-progress" {
			fmt.Fprintln(os.Stdout, "not json")
		}
	}
	if mode == "agent-result-fail" {
		r.Outcome = eval.Errored
		r.Error = "no model call completed: connection refused"
	}
	b, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(results, "results.jsonl"), append(b, '\n'), 0600)
	_ = os.WriteFile(filepath.Join(results, id, "result.json"), b, 0600)
	if mode == "agent-result-fail" {
		fmt.Fprintln(os.Stderr, "run stopped; artifacts retained")
		os.Exit(1)
	}
	os.Exit(0)
}

func calls(t *testing.T, state string) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args)
	}
	return out
}

func testContainerTask(t *testing.T, mode string) (*Runner, *job, eval.Task, string, string, containerInputs) {
	t.Helper()
	r, state := fakeContainer(t, mode)
	tasks, err := eval.LoadProblems(r.Ladder)
	if err != nil {
		t.Fatal(err)
	}
	var task eval.Task
	for _, v := range tasks {
		if v.ID == "easy-01-budget-pair" {
			task = v
		}
	}
	dir := t.TempDir()
	inputs, err := r.prepareContainers(context.Background(), dir, []eval.Task{task})
	if err != nil {
		t.Fatal(err)
	}
	j := &job{id: "owned-test", status: "running", byID: map[string]*TaskState{task.ID: {ID: task.ID}}, agents: map[string]map[message.ActorID]bool{}, changed: make(chan struct{})}
	return r, j, task, filepath.Join(dir, task.ID), state, inputs
}

func TestContainerTaskMountsAndProgress(t *testing.T) {
	r, j, task, attempt, state, inputs := testContainerTask(t, "")
	result, err := r.containerTask(context.Background(), j, task, attempt, inputs)
	if err != nil || result.Outcome != eval.Failed {
		t.Fatal(result, err)
	}
	all := calls(t, state)
	if len(all) != 2 {
		t.Fatal(all)
	}
	agent, grader := strings.Join(all[0], "\n"), strings.Join(all[1], "\n")
	for _, part := range []string{"target=/workspace", "target=/results", "target=/outbox", "target=/config,readonly", "target=/problems,readonly", "/config/run.json", "-progress-json"} {
		if !strings.Contains(agent, part) {
			t.Fatal("agent missing", part, agent)
		}
	}
	if strings.Contains(agent, "target=/grading") {
		t.Fatal("agent received grading fixtures")
	}
	for _, part := range []string{"--network\nnone", "target=/outbox,readonly", "target=/grading,readonly", "target=/results"} {
		if !strings.Contains(grader, part) {
			t.Fatal("grader missing", part, grader)
		}
	}
	for _, part := range []string{"target=/workspace", "target=/config", "target=/problems"} {
		if strings.Contains(grader, part) {
			t.Fatal("grader received", part)
		}
	}
	if j.byID[task.ID].ModelCalls != 1 || j.byID[task.ID].OutputTokens != 20 {
		t.Fatal(j.byID[task.ID])
	}
}

func TestContainerFailureNeverFallsBackToHostOrGrades(t *testing.T) {
	for _, mode := range []string{"agent-fail", "no-submission", "bad-progress"} {
		t.Run(mode, func(t *testing.T) {
			r, j, task, attempt, state, inputs := testContainerTask(t, mode)
			_, err := r.containerTask(context.Background(), j, task, attempt, inputs)
			if err == nil {
				t.Fatal("failure accepted")
			}
			if got := calls(t, state); len(got) != 1 {
				t.Fatal("unexpected grader/fallback", got)
			}
		})
	}
}

func TestContainerCancellationStopsOnlyOwnedAgent(t *testing.T) {
	r, j, task, attempt, state, inputs := testContainerTask(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.containerTask(ctx, j, task, attempt, inputs); done <- err }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(state, "started")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("agent did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel hung")
	}
	got := calls(t, state)
	if len(got) != 3 {
		t.Fatal(got)
	}
	name := "strap-eval-web-owned-test-" + task.ID + "-agent"
	if strings.Join(got[1], " ") != "stop "+name || strings.Join(got[2], " ") != "delete "+name {
		t.Fatal(got)
	}
}

func TestContainerGraderFailurePreservesSubmission(t *testing.T) {
	r, j, task, attempt, state, inputs := testContainerTask(t, "grader-fail")
	_, err := r.containerTask(context.Background(), j, task, attempt, inputs)
	if err == nil {
		t.Fatal("grader failure accepted")
	}
	if len(calls(t, state)) != 2 {
		t.Fatal("unexpected runtime calls")
	}
	if _, err := os.Stat(filepath.Join(attempt, "outbox/submission/manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestContainerBuildFailureStopsPreparation(t *testing.T) {
	r, state := fakeContainer(t, "build-fail")
	r.NoBuild = false
	tasks, err := eval.LoadProblems(r.Ladder)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, err = r.prepareContainers(context.Background(), dir, tasks[:1])
	if err == nil || !strings.Contains(err.Error(), "build eval image") {
		t.Fatalf("expected build failure, got %v", err)
	}
	got := calls(t, state)
	if len(got) != 1 || got[0][0] != "build" {
		t.Fatalf("unexpected execution after failed build: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "build.log")); err != nil {
		t.Fatal(err)
	}
}

func TestContainerFailureSurfacesLogAndPreservesResult(t *testing.T) {
	for _, mode := range []string{"agent-fail", "agent-result-fail"} {
		t.Run(mode, func(t *testing.T) {
			r, j, task, attempt, state, inputs := testContainerTask(t, mode)
			result, err := r.containerTask(context.Background(), j, task, attempt, inputs)
			if err == nil || !strings.Contains(err.Error(), "agent.log") {
				t.Fatal("missing stderr diagnostic", err)
			}
			if mode == "agent-fail" && !strings.Contains(err.Error(), "agent failed") {
				t.Fatal(err)
			}
			if mode == "agent-result-fail" && (result.TaskID != task.ID || result.Outcome != eval.Errored || !strings.Contains(err.Error(), "connection refused")) {
				t.Fatal("saved result discarded", result, err)
			}
			if len(calls(t, state)) != 1 {
				t.Fatal("failed agent unexpectedly graded")
			}
		})
	}
}

func TestContainerFailureBoundsLogTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.log"), []byte(strings.Repeat("x", 20000)+"\nactual cause"), 0600); err != nil {
		t.Fatal(err)
	}
	err := containerFailure(dir, "agent", fmt.Errorf("exit status 1"))
	if len(err.Error()) > 4300 || !strings.Contains(err.Error(), "actual cause") {
		t.Fatal("log tail is missing or unbounded", len(err.Error()))
	}
}
