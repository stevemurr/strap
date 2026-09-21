package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Config  harness.Config
	Deps    harness.Dependencies // Provider injection for tests; EventStore must stay nil.
	Mounts  Mounts
	Problem string
	Log     io.Writer
	Observe func(Progress) // Optional live observer; see Progress for concurrency contract.
	Quiet   time.Duration  // Silence required after the root's final reply; default 3s.
	// Idle finishes a task whose agents are all idle with nothing queued and no
	// root reply after this much silence, flagged NoReply; default 3m. Without
	// it a root that ends on wait_for_input costs the whole session budget.
	Idle time.Duration
	// Commit and Profile are recorded in run.json so a run can be traced to
	// the harness build and the model profile that produced it.
	Commit  string
	Profile string
}

// Outcome classifies a task attempt by its grade, not by how the session ended.
type Outcome string

const (
	Submitted   Outcome = "submitted"
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
	Mounts    Mounts              `json:"mounts"`
	Model     harness.ModelConfig `json:"model"`
	Tasks     []string            `json:"tasks"`
}

const replyLimit = 4 << 10

// Run executes one problem in the mounted workspace. Each invocation requires
// fresh output mounts; container orchestration owns retries and parallelism.
func Run(ctx context.Context, opts Options) (results []Result, runErr error) {
	if opts.Deps.EventStore != nil {
		return nil, errors.New("the runner records its own JSONL trace; EventStore must be nil")
	}
	if opts.Problem == "" {
		return nil, errors.New("problem is required: run one problem per container")
	}
	var err error
	opts.Mounts, err = opts.Mounts.resolve(false)
	if err != nil {
		return nil, err
	}
	tasks, err := LoadProblems(opts.Mounts.Problems)
	if err != nil {
		return nil, err
	}
	var task Task
	for _, candidate := range tasks {
		if candidate.ID == opts.Problem {
			task = candidate
			break
		}
	}
	if task.ID == "" {
		return nil, fmt.Errorf("unknown problem %q", opts.Problem)
	}
	// Validate both mounts before writing either. Never delete mounted contents.
	for _, dir := range []string{opts.Mounts.Workspace, opts.Mounts.Results, opts.Mounts.Outbox} {
		if err := requireEmpty(dir); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info := RunInfo{StartedAt: time.Now(), Commit: opts.Commit, Profile: opts.Profile, Mounts: opts.Mounts, Model: opts.Config.Model, Tasks: []string{task.ID}}
	if err := writeJSON(filepath.Join(opts.Mounts.Results, "run.json"), info); err != nil {
		return nil, err
	}
	opts.notify(Progress{Task: task, Phase: Queued})
	r := runTask(ctx, opts, task)
	results = []Result{r}
	defer func() {
		if runErr != nil && results[0].Outcome == Submitted {
			results[0].Outcome, results[0].Error = Errored, runErr.Error()
		}
		opts.notify(Progress{Task: task, Phase: Finished, Result: &results[0]})
	}()
	// Interrupted attempts retain their workspace and trace, but aren't grades.
	if err := ctx.Err(); err != nil {
		return results, err
	}
	if err := saveResult(opts.Mounts.Results, r); err != nil {
		return results, err
	}
	if r.Outcome == Errored {
		return results, errors.New(r.Error)
	}
	if r.Outcome == Submitted {
		if err := publishSubmission(ctx, opts.Mounts, info, r); err != nil {
			r.Outcome, r.Error = Errored, fmt.Sprintf("publish submission: %v", err)
			results[0] = r
			return results, errors.Join(err, saveResult(opts.Mounts.Results, r))
		}
	}
	return results, nil
}

// runTask leaves the workspace mounted in place, including on startup failure
// or cancellation. Only closed sessions can be published to the outbox.
func runTask(ctx context.Context, opts Options, task Task) (r Result) {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Quiet <= 0 {
		opts.Quiet = 3 * time.Second
	}
	if opts.Idle <= 0 {
		opts.Idle = 3 * time.Minute
	}
	taskDir := filepath.Join(opts.Mounts.Results, task.ID)
	r = Result{TaskID: task.ID, Tier: task.Tier, Title: task.Title, StartedAt: time.Now(), Trace: filepath.Join(taskDir, "trace.jsonl"), Workspace: opts.Mounts.Workspace}
	opts.notify(Progress{Task: task, Phase: Starting})
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
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return fail(err)
	}
	workspace := opts.Mounts.Workspace
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
	drain := opts.observeSession(session, task)
	cleanup := func() error {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		closeErr := session.Close(closeCtx)
		drain(closeCtx)
		insp := session.Inspect()
		capture := insp.Capture
		r.Capture = &capture
		if insp.Outcome != nil {
			r.ExecutionError = insp.Outcome.Error
		}
		return errors.Join(closeErr, session.Dispose(closeCtx))
	}
	sub, err := session.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		return fail(errors.Join(err, cleanup()))
	}
	if _, err := session.Send(session.Root(), task.Prompt); err != nil {
		return fail(errors.Join(err, cleanup()))
	}
	w := watcher{root: session.Root(), states: map[identity.ActorID]agent.State{}, working: map[identity.ActorID]bool{}, tools: map[string]bool{}, pending: map[message.MessageID]identity.ActorID{}}
	waitErr := w.wait(ctx, session, sub, task.SessionTimeout(), opts.Quiet, opts.Idle, func(format string, args ...any) {
		fmt.Fprintf(opts.Log, "[%s] %s\n", task.ID, fmt.Sprintf(format, args...))
	})
	r.TimedOut = errors.Is(waitErr, errBudget)
	r.NoReply = errors.Is(waitErr, errNoReply)
	r.Replies, r.Reply = w.replies, bound(w.lastReply, replyLimit)
	if err := cleanup(); err != nil {
		return fail(err)
	}
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
	r.Outcome = Submitted
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
