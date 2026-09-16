package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
)

// Options configure a run. Config supplies the model and role prompts; the
// runner owns Dir, Events.JSONLPath, Web and LocalTools for every task.
type Options struct {
	Config   harness.Config
	Deps     harness.Dependencies // Provider injection for tests; EventStore must stay nil.
	Ladder   string
	Output   string
	Parallel int
	Filter   func(Task) bool
	Log      io.Writer
	Quiet    time.Duration // Silence required after the root's final reply; default 3s.
	// Idle finishes a task whose agents are all idle with nothing queued and no
	// root reply after this much silence, flagged NoReply; default 3m. Without
	// it a root that ends on wait_for_input costs the whole session budget.
	Idle time.Duration
	// Scratch holds each live workspace while its session runs; default
	// os.TempDir(). Keeping sessions out of the repository tree stops an agent
	// from finding the ladder, its hidden tests and reference solutions by
	// walking up from its working directory. The workspace moves under Output
	// once the task is graded.
	Scratch string
	// Commit and Profile are recorded in run.json so a run can be traced to
	// the harness build and the model profile that produced it.
	Commit  string
	Profile string
}

// Outcome classifies a task attempt by its grade, not by how the session ended.
type Outcome string

const (
	Passed      Outcome = "passed"
	Failed      Outcome = "failed"
	BuildFailed Outcome = "build_failed"
	Errored     Outcome = "error"
)

type Result struct {
	TaskID         string           `json:"task_id"`
	Tier           string           `json:"tier"`
	Title          string           `json:"title"`
	Outcome        Outcome          `json:"outcome"`
	Passed         bool             `json:"passed"`
	TimedOut       bool             `json:"timed_out"`
	NoReply        bool             `json:"no_reply"`
	Error          string           `json:"error,omitempty"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	Duration       time.Duration    `json:"duration_ns"`
	Session        string           `json:"session,omitempty"`
	Trace          string           `json:"trace"`
	Workspace      string           `json:"workspace"`
	Replies        int              `json:"replies"`
	Reply          string           `json:"reply,omitempty"`
	Grade          *Grade           `json:"grade,omitempty"`
	Capture        *eventlog.Status `json:"capture,omitempty"`
	ExecutionError string           `json:"execution_error,omitempty"`
}

// RunInfo is written to run.json when a run starts.
type RunInfo struct {
	StartedAt time.Time           `json:"started_at"`
	Commit    string              `json:"commit,omitempty"`
	Profile   string              `json:"profile,omitempty"`
	Ladder    string              `json:"ladder"`
	Model     harness.ModelConfig `json:"model"`
	Parallel  int                 `json:"parallel"`
	Tasks     []string            `json:"tasks"`
}

const replyLimit = 4 << 10

// Run executes every selected task and returns results in ladder order. Tasks
// with an existing result.json under Output are reused, so an interrupted run
// resumes by pointing at the same directory.
func Run(ctx context.Context, opts Options) ([]Result, error) {
	if opts.Deps.EventStore != nil {
		return nil, errors.New("the runner records each task to its own JSONL trace; EventStore must be nil")
	}
	if opts.Parallel < 1 {
		opts.Parallel = 1
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Quiet <= 0 {
		opts.Quiet = 3 * time.Second
	}
	if opts.Idle <= 0 {
		opts.Idle = 3 * time.Minute
	}
	tasks, err := LoadLadder(opts.Ladder)
	if err != nil {
		return nil, err
	}
	if opts.Filter != nil {
		kept := tasks[:0]
		for _, t := range tasks {
			if opts.Filter(t) {
				kept = append(kept, t)
			}
		}
		tasks = kept
	}
	if len(tasks) == 0 {
		return nil, errors.New("no tasks selected")
	}
	if err := os.MkdirAll(opts.Output, 0o755); err != nil {
		return nil, err
	}
	info := RunInfo{StartedAt: time.Now(), Commit: opts.Commit, Profile: opts.Profile, Ladder: opts.Ladder, Model: opts.Config.Model, Parallel: opts.Parallel}
	for _, t := range tasks {
		info.Tasks = append(info.Tasks, t.ID)
	}
	if _, err := os.Stat(filepath.Join(opts.Output, "run.json")); errors.Is(err, os.ErrNotExist) {
		if err := writeJSON(filepath.Join(opts.Output, "run.json"), info); err != nil {
			return nil, err
		}
	}
	lines, err := os.OpenFile(filepath.Join(opts.Output, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer lines.Close()
	var mu sync.Mutex
	results := make([]Result, len(tasks))
	queue := make(chan int)
	var wg sync.WaitGroup
	for range opts.Parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				task := tasks[i]
				resultPath := filepath.Join(opts.Output, task.ID, "result.json")
				if data, err := os.ReadFile(resultPath); err == nil {
					var prev Result
					if json.Unmarshal(data, &prev) == nil && prev.TaskID == task.ID {
						fmt.Fprintf(opts.Log, "[%s] reusing %s\n", task.ID, resultPath)
						results[i] = prev
						continue
					}
				}
				if ctx.Err() != nil {
					results[i] = Result{TaskID: task.ID, Tier: task.Tier, Title: task.Title, Outcome: Errored, Error: ctx.Err().Error()}
					continue
				}
				r := RunTask(ctx, opts, task)
				results[i] = r
				if ctx.Err() != nil {
					continue // Interrupted attempts are not durable results.
				}
				if err := writeJSON(resultPath, r); err != nil {
					fmt.Fprintf(opts.Log, "[%s] write result: %v\n", task.ID, err)
				}
				line, _ := json.Marshal(r)
				mu.Lock()
				_, _ = lines.Write(append(line, '\n'))
				mu.Unlock()
			}
		}()
	}
	for i := range tasks {
		queue <- i
	}
	close(queue)
	wg.Wait()
	return results, ctx.Err()
}

// RunTask runs one task. The agent works in a temporary directory under
// Scratch; afterwards <Output>/<task id>/ holds that workspace with the hidden
// tests copied in, trace.jsonl and result.json.
func RunTask(ctx context.Context, opts Options, task Task) (r Result) {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Quiet <= 0 {
		opts.Quiet = 3 * time.Second
	}
	if opts.Idle <= 0 {
		opts.Idle = 3 * time.Minute
	}
	taskDir := filepath.Join(opts.Output, task.ID)
	r = Result{TaskID: task.ID, Tier: task.Tier, Title: task.Title, StartedAt: time.Now(), Trace: filepath.Join(taskDir, "trace.jsonl"), Workspace: filepath.Join(taskDir, "workspace")}
	defer func() {
		r.FinishedAt = time.Now()
		r.Duration = r.FinishedAt.Sub(r.StartedAt)
		r.Passed = r.Outcome == Passed
		fmt.Fprintf(opts.Log, "[%s] %s after %s\n", task.ID, r.Outcome, r.Duration.Round(time.Second))
	}()
	fail := func(err error) Result {
		r.Outcome, r.Error = Errored, err.Error()
		return r
	}
	if err := os.RemoveAll(r.Workspace); err != nil {
		return fail(err)
	}
	if err := os.Remove(r.Trace); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return fail(err)
	}
	scratch := opts.Scratch
	if scratch == "" {
		scratch = os.TempDir()
	}
	live, err := os.MkdirTemp(scratch, "strap-eval-"+task.ID+"-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(live)
	workspace := filepath.Join(live, "workspace")
	defer func() {
		// Keep whatever the agent left, graded or not, beside the trace.
		if err := os.Rename(workspace, r.Workspace); err != nil {
			if err := copyTree(workspace, r.Workspace); err != nil {
				fmt.Fprintf(opts.Log, "[%s] keep workspace: %v\n", task.ID, err)
			}
		}
	}()
	if err := Materialize(task, workspace); err != nil {
		return fail(err)
	}
	cfg := opts.Config.Clone()
	cfg.Dir = workspace
	cfg.Web = nil
	cfg.LocalTools = true
	cfg.Events.JSONLPath = r.Trace
	fmt.Fprintf(opts.Log, "[%s] starting %q (budget %s)\n", task.ID, task.Title, task.SessionTimeout())
	session, err := harness.New(ctx, cfg, opts.Deps)
	if err != nil {
		var startup *harness.StartupError
		if errors.As(err, &startup) {
			err = errors.Join(err, startup.Close(context.Background()))
		}
		return fail(err)
	}
	r.Session = session.ID()
	cleanup := func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := session.Close(closeCtx); err != nil {
			fmt.Fprintf(opts.Log, "[%s] close: %v\n", task.ID, err)
		}
		insp := session.Inspect()
		capture := insp.Capture
		r.Capture = &capture
		if insp.Outcome != nil {
			r.ExecutionError = insp.Outcome.Error
		}
		if err := session.Dispose(closeCtx); err != nil {
			fmt.Fprintf(opts.Log, "[%s] dispose: %v\n", task.ID, err)
		}
	}
	sub, err := session.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		cleanup()
		return fail(err)
	}
	if _, err := session.Send(session.Root(), task.Prompt); err != nil {
		cleanup()
		return fail(err)
	}
	w := watcher{root: session.Root(), states: map[identity.ActorID]agent.State{}, working: map[identity.ActorID]bool{}, tools: map[string]bool{}, pending: map[message.MessageID]identity.ActorID{}}
	waitErr := w.wait(ctx, session, sub, task.SessionTimeout(), opts.Quiet, opts.Idle, func(format string, args ...any) {
		fmt.Fprintf(opts.Log, "[%s] %s\n", task.ID, fmt.Sprintf(format, args...))
	})
	r.TimedOut = errors.Is(waitErr, errBudget)
	r.NoReply = errors.Is(waitErr, errNoReply)
	r.Replies, r.Reply = w.replies, bound(w.lastReply, replyLimit)
	cleanup()
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if waitErr != nil && !r.TimedOut && !r.NoReply {
		return fail(waitErr)
	}
	if w.completed == 0 && r.ExecutionError != "" {
		// The model never answered once: an unreachable server or a bad
		// endpoint, not something the workspace can be graded on.
		return fail(fmt.Errorf("no model call completed: %s", r.ExecutionError))
	}
	if err := ApplyHidden(task, workspace); err != nil {
		return fail(err)
	}
	grade, err := RunHiddenTests(ctx, task, workspace)
	if err != nil {
		return fail(err)
	}
	r.Grade = &grade
	switch {
	case grade.Passed:
		r.Outcome = Passed
	case !grade.Compiled:
		r.Outcome = BuildFailed
	default:
		r.Outcome = Failed
	}
	return r
}

var errBudget = errors.New("session budget exhausted")

var errNoReply = errors.New("session idle without a root reply")

// watcher decides when a task attempt is finished: the root has replied to the
// user and no agent is running, no tool call is open, and no message is still
// queued for a live agent. Those signals are the same ones the TUI uses for
// its activity indicator.
type watcher struct {
	root      identity.ActorID
	states    map[identity.ActorID]agent.State
	working   map[identity.ActorID]bool
	tools     map[string]bool
	pending   map[message.MessageID]identity.ActorID
	replied   bool
	replies   int
	lastReply string
	completed int // Model calls that produced a response.
}

func (w *watcher) busy() bool {
	if len(w.working) > 0 || len(w.tools) > 0 {
		return true
	}
	for _, recipient := range w.pending {
		state := w.states[recipient]
		if state != agent.Paused && !state.Terminal() {
			return true
		}
	}
	return false
}

func (w *watcher) done() bool { return w.replied && !w.busy() }

func (w *watcher) observe(e conversation.Event, logf func(string, ...any)) {
	switch e := e.(type) {
	case conversation.AgentStarted:
		w.states[e.Agent.ID] = e.Agent.State
	case conversation.AgentStateChanged:
		w.states[e.Agent] = e.State
		if e.State == agent.Running || e.State == agent.PauseRequested {
			w.working[e.Agent] = true
		} else {
			delete(w.working, e.Agent)
		}
	case conversation.AgentExited:
		w.states[e.Agent] = agent.Stopped
		delete(w.working, e.Agent)
		if e.Err != nil {
			logf("agent %s exited: %v", e.Agent, e.Err)
		}
	case conversation.ToolEvent:
		if e.Activity.FinishedAt.IsZero() {
			w.tools[e.Activity.InvocationID] = true
		} else {
			delete(w.tools, e.Activity.InvocationID)
		}
	case conversation.AckEvent:
		switch e.Receipt.Status {
		case message.Queued:
			if e.Receipt.Recipient != message.User {
				w.pending[e.Receipt.MessageID] = e.Receipt.Recipient
			}
		case message.Consumed:
			delete(w.pending, e.Receipt.MessageID)
			w.working[e.Receipt.Recipient] = true
		case message.Undelivered:
			delete(w.pending, e.Receipt.MessageID)
			logf("%s undelivered: %s", e.Receipt.MessageID, e.Receipt.Detail)
		}
	case conversation.AgentEvent:
		if f, ok := e.Event.(agent.OutputFinished); ok && f.Status == agent.OutputComplete {
			w.completed++
		}
	case conversation.MessageEvent:
		m := e.Message
		if m.To != message.User {
			w.pending[m.ID] = m.To
		}
		if m.Kind == message.Reply || m.Kind == message.Failure {
			delete(w.working, m.From)
		}
		if m.From == w.root && m.To == message.User && (m.Kind == message.Reply || m.Kind == message.Failure) {
			w.replied = true
			w.replies++
			w.lastReply = m.Content
			logf("root %s #%d (%d bytes)", m.Kind, w.replies, len(m.Content))
		}
	}
}

// Only these kinds influence completion; streamed output is left undecoded.
var watchedKinds = map[string]bool{"agent_started": true, "agent_state": true, "agent_exited": true, "tool": true, "ack": true, "message": true, "output_finished": true}

func (w *watcher) wait(ctx context.Context, session *harness.Session, sub *eventlog.Subscription, budget, quiet, idle time.Duration, logf func(string, ...any)) error {
	deadline := time.Now().Add(budget)
	last := time.Now()
	for {
		limit := deadline
		switch {
		case w.done():
			limit = time.Now().Add(quiet)
		case !w.busy() && !w.replied:
			if silent := last.Add(idle); silent.Before(limit) {
				limit = silent
			}
		}
		waitCtx, cancel := context.WithDeadline(ctx, limit)
		e, err := sub.Next(waitCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				switch {
				case w.done():
					return nil
				case !w.busy() && !w.replied && time.Since(last) >= idle:
					logf("idle for %s without a root reply", idle)
					return errNoReply
				}
				return errBudget
			}
			return fmt.Errorf("read session events: %w", err)
		}
		last = time.Now()
		if !watchedKinds[e.Kind] {
			continue
		}
		resolved, err := session.ResolveRecord(ctx, e)
		if err != nil {
			return fmt.Errorf("resolve event %d: %w", e.Sequence, err)
		}
		v, err := eventcodec.DecodeEvent(resolved)
		if err != nil {
			logf("decode event %d (%s): %v", e.Sequence, e.Kind, err)
			continue
		}
		if v != nil {
			w.observe(v, logf)
		}
	}
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
