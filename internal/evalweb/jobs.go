package evalweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
)

// Runner is what the page needs to launch evals on this host: the model and
// harness configuration the CLI resolved, and the private ladder that holds
// both the public problems and the hidden tests.
type Runner struct {
	Config       harness.Config
	Ladder       string
	Profile      string
	Commit       string
	Quiet        time.Duration
	Idle         time.Duration
	Image        string
	BuildContext string
	NoBuild      bool
	command      func(context.Context, ...string) *exec.Cmd // isolated runtime in tests
}

// JobEvent is one line of progress. Task-level events carry the task id;
// job-level ones leave it empty.
type JobEvent struct {
	Seq    int       `json:"seq"`
	At     time.Time `json:"at"`
	Task   string    `json:"task,omitempty"`
	Kind   string    `json:"kind"`
	Agent  string    `json:"agent,omitempty"`
	Parent string    `json:"parent,omitempty"`
	Text   string    `json:"text,omitempty"`
	Phase  string    `json:"phase,omitempty"`
}

type AgentContext struct {
	Revision uint64 `json:"revision"`
	Tokens   int64  `json:"tokens"`
	Error    string `json:"error,omitempty"`
}

// TaskState is the live and final view of one task in a job.
type TaskState struct {
	Context      map[string]AgentContext `json:"context,omitempty"`
	ID           string                  `json:"id"`
	Tier         string                  `json:"tier"`
	Title        string                  `json:"title"`
	Phase        string                  `json:"phase"`
	StartedAt    *time.Time              `json:"started_at,omitempty"`
	FinishedAt   *time.Time              `json:"finished_at,omitempty"`
	Outcome      string                  `json:"outcome,omitempty"`
	Passed       bool                    `json:"passed"`
	TimedOut     bool                    `json:"timed_out"`
	NoReply      bool                    `json:"no_reply"`
	Error        string                  `json:"error,omitempty"`
	ModelCalls   int                     `json:"model_calls"`
	ToolCalls    int                     `json:"tool_calls"`
	ToolErrors   int                     `json:"tool_errors"`
	InputTokens  int64                   `json:"input_tokens"`
	OutputTokens int64                   `json:"output_tokens"`
	Agents       int                     `json:"agents"`
	Results      string                  `json:"results,omitempty"`
}

// JobSnapshot is the JSON view of a job.
type JobSnapshot struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Dir        string       `json:"dir"`
	Status     string       `json:"status"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Error      string       `json:"error,omitempty"`
	Tasks      []*TaskState `json:"tasks"`
	Events     int          `json:"events"`
	Metadata   RunMetadata  `json:"metadata"`
}

type job struct {
	id, name, dir string
	runner        *Runner
	metadata      RunMetadata
	cancel        context.CancelFunc

	mu       sync.Mutex
	status   string
	started  time.Time
	finished *time.Time
	err      string
	tasks    []*TaskState
	byID     map[string]*TaskState
	agents   map[string]map[message.ActorID]bool
	events   []JobEvent
	changed  chan struct{}
}

// maxJobEvents bounds a job's buffered progress. Output deltas are never
// forwarded, so a task produces a few hundred events at most.
const maxJobEvents = 50000

func (j *job) snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	tasks := make([]*TaskState, len(j.tasks))
	for i, t := range j.tasks {
		copied := *t
		if t.Context != nil {
			copied.Context = make(map[string]AgentContext, len(t.Context))
			for id, c := range t.Context {
				copied.Context[id] = c
			}
		}
		tasks[i] = &copied
	}
	return JobSnapshot{ID: j.id, Name: j.name, Dir: j.dir, Status: j.status, StartedAt: j.started, FinishedAt: j.finished, Error: j.err, Tasks: tasks, Events: len(j.events), Metadata: j.metadata}
}

func (j *job) emit(e JobEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.emitLocked(e)
}

func (j *job) emitLocked(e JobEvent) {
	if len(j.events) >= maxJobEvents {
		return
	}
	e.Seq = len(j.events) + 1
	if e.At.IsZero() {
		e.At = time.Now()
	}
	j.events = append(j.events, e)
	close(j.changed)
	j.changed = make(chan struct{})
}

// after returns buffered events past seq and a channel that closes on the
// next append, so a follower can wait without polling.
func (j *job) after(seq int) ([]JobEvent, <-chan struct{}, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []JobEvent
	if seq < len(j.events) {
		out = append(out, j.events[seq:]...)
	}
	return out, j.changed, j.status != "running"
}

// observe maps the runner's progress onto job events and live counters.
func (j *job) observe(p eval.Progress) {
	j.mu.Lock()
	defer j.mu.Unlock()
	t := j.byID[p.Task.ID]
	if t == nil {
		return
	}
	if p.Result != nil {
		return // The runner's final result is applied by run once grading is done.
	}
	if p.Event == nil {
		if string(p.Phase) != t.Phase {
			t.Phase = string(p.Phase)
			if p.Phase == eval.Starting && t.StartedAt == nil {
				at := p.At
				t.StartedAt = &at
			}
			j.emitLocked(JobEvent{At: p.At, Task: t.ID, Kind: "phase", Phase: t.Phase})
		}
		return
	}
	e := JobEvent{At: p.At, Task: t.ID}
	switch ev := p.Event.(type) {
	case conversation.ToolEvent:
		e.Kind, e.Agent = "tool", string(ev.Agent)
		name := ev.Activity.Call.Name
		args := strings.TrimSpace(string(ev.Activity.Call.Arguments))
		args = clip(args, 16384)
		switch {
		case ev.Activity.FinishedAt.IsZero():
			e.Text = name + "\n```json\n" + args + "\n```"
		case ev.Activity.Err != nil:
			t.ToolCalls++
			t.ToolErrors++
			e.Kind, e.Text = "tool_error", name+": "+ev.Activity.Err.Error()
		default:
			t.ToolCalls++
			e.Kind, e.Text = "tool_done", name+" "+ev.Activity.FinishedAt.Sub(ev.Activity.StartedAt).Round(time.Millisecond).String()
			if output := ev.Activity.Result.Content.Text(); output != "" {
				e.Text += "\n```\n" + clip(output, 16384) + "\n```"
			}
		}
	case conversation.MessageEvent:
		m := ev.Message
		e.Kind, e.Agent, e.Text = "message", string(m.From), fmt.Sprintf("%s → %s (%s): %s", m.From, m.To, m.Kind, clip(m.Content, 16384))
	case conversation.CommentaryEvent:
		e.Kind, e.Agent, e.Text = "commentary", string(ev.Agent), clip(ev.Content, 16384)
	case conversation.AgentStarted:
		e.Kind, e.Agent, e.Text = "agent", string(ev.Agent.ID), "started"
		e.Parent = string(ev.Agent.Parent)
		agents := j.agents[t.ID]
		if agents == nil {
			agents = map[message.ActorID]bool{}
			j.agents[t.ID] = agents
		}
		agents[ev.Agent.ID] = true
		t.Agents = len(agents)
	case conversation.AgentExited:
		e.Kind, e.Agent, e.Text = "agent", string(ev.Agent), "exited"
		if ev.Err != nil {
			e.Text += ": " + ev.Err.Error()
		}
	case conversation.AgentStateChanged:
		e.Kind, e.Agent, e.Text = "state", string(ev.Agent), string(ev.State)
	case conversation.UsageEvent:
		t.ModelCalls++
		e.Kind, e.Agent = "usage", string(ev.Agent)
		if u := ev.Observation.Usage; u != nil && u.InputTokens != nil && u.OutputTokens != nil {
			t.InputTokens += *u.InputTokens
			t.OutputTokens += *u.OutputTokens
			e.Text = fmt.Sprintf("in %d · out %d", *u.InputTokens, *u.OutputTokens)
		} else {
			e.Text = "usage unknown"
		}
	case conversation.ContextTokensEvent:
		if t.Context == nil {
			t.Context = map[string]AgentContext{}
		}
		previous, exists := t.Context[string(ev.Agent)]
		if exists && ev.Revision < previous.Revision {
			return
		}
		t.Context[string(ev.Agent)] = AgentContext{Revision: ev.Revision, Tokens: ev.Count, Error: ev.Error}
		e.Kind, e.Agent = "context_tokens", string(ev.Agent)
		if ev.Error != "" {
			e.Text = "Context measurement unavailable: " + ev.Error
		} else {
			e.Text = fmt.Sprintf("%d context tokens", ev.Count)
		}
	case conversation.DiagnosticEvent:
		e.Kind, e.Text = "diagnostic", ev.Level+": "+ev.Message
	case conversation.WorkEvent:
		e.Kind, e.Text = "work", string(ev.Event.Kind)
	default:
		return
	}
	j.emitLocked(e)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// run executes the tasks in order. Each task gets fresh workspace, results and
// outbox mounts beneath the batch. Execution and grading occur only in separate
// containers; the host reads artifacts and relays progress.
func (j *job) run(ctx context.Context, s *Server, tasks []eval.Task) {
	r := j.runner
	if r == nil {
		r = s.runner
	}
	defer func() {
		j.mu.Lock()
		now := time.Now()
		j.finished = &now
		if j.status == "running" {
			j.status = "done"
			if ctx.Err() != nil {
				j.status = "cancelled"
			}
		}
		j.metadata.Status = j.status
		if err := writeMetadata(filepath.Join(s.root, j.dir), j.metadata, "eval-run.json"); err != nil {
			j.err = "save metadata: " + err.Error()
		}
		j.emitLocked(JobEvent{Kind: "job", Text: j.status})
		j.mu.Unlock()
		s.invalidate()
	}()
	j.emit(JobEvent{Kind: "job", Text: "Preparing eval containers"})
	inputs, prepareErr := r.prepareContainers(ctx, filepath.Join(s.root, j.dir), tasks, j.emit)
	if prepareErr != nil {
		j.emit(JobEvent{Kind: "build", Phase: "failed", Text: prepareErr.Error()})
		j.mu.Lock()
		j.err = prepareErr.Error()
		j.mu.Unlock()
		for _, task := range tasks {
			j.fail(j.byID[task.ID], prepareErr)
		}
		return
	}
	for _, task := range tasks {
		t := j.byID[task.ID]
		if ctx.Err() != nil {
			j.mu.Lock()
			t.Phase, t.Error = "cancelled", "cancelled before start"
			j.mu.Unlock()
			continue
		}
		attempt := filepath.Join(s.root, j.dir, task.ID)
		resultsDir := filepath.Join(attempt, "results")
		result, err := r.containerTask(ctx, j, task, attempt, inputs)
		if report, analyzeErr := eval.Analyze(ctx, resultsDir); analyzeErr == nil {
			_ = eval.WriteReport(report)
		}
		rel, _ := filepath.Rel(s.root, resultsDir)
		j.mu.Lock()
		now := time.Now()
		t.FinishedAt, t.Phase, t.Results = &now, string(eval.Finished), filepath.ToSlash(rel)
		if result.TaskID != "" {
			t.Outcome, t.Passed, t.TimedOut, t.NoReply = string(result.Outcome), result.Passed, result.TimedOut, result.NoReply
			if result.Error != "" {
				t.Error = result.Error
			}
		}
		if err != nil {
			t.Error = err.Error()
			if t.Outcome == "" {
				t.Outcome = string(eval.Errored)
			}
		}
		j.emitLocked(JobEvent{Task: t.ID, Kind: "result", Text: t.Outcome, Phase: t.Phase})
		j.mu.Unlock()
	}
}

func (j *job) fail(t *TaskState, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	t.FinishedAt, t.Phase, t.Outcome, t.Error = &now, string(eval.Finished), string(eval.Errored), err.Error()
	j.emitLocked(JobEvent{Task: t.ID, Kind: "result", Text: t.Outcome, Phase: t.Phase})
}

func mkdirs(dirs ...string) error {
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// startJob launches one job; only one runs at a time because every task
// shares the model endpoint and host resource budget.
func (s *Server) startJob(ids []string, options ...newEval) (*job, error) {
	if s.runner == nil {
		return nil, errors.New("running is disabled: start strap eval web with model flags and a ladder to enable it")
	}
	request := newEval{}
	if len(options) > 0 {
		request = options[0]
	}
	configured, err := s.runner.configured(request)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errors.New("choose at least one task")
	}
	all, err := eval.LoadProblems(s.runner.Ladder)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var tasks []eval.Task
	for _, t := range all {
		if want[t.ID] {
			tasks = append(tasks, t)
			delete(want, t.ID)
		}
	}
	if len(want) != 0 {
		var unknown []string
		for id := range want {
			unknown = append(unknown, id)
		}
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown tasks: %s", strings.Join(unknown, ", "))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.jobs {
		if existing.snapshot().Status == "running" {
			return nil, errors.New("a job is already running; cancel it or wait for it to finish")
		}
	}
	now := time.Now()
	name := fmt.Sprintf("eval-%d", now.UnixNano())
	display := strings.TrimSpace(request.Name)
	if display == "" {
		display = defaultEvalName(configured.Config.Model.Model, tasks)
	}
	metadata := configured.metadata(display, now)
	if err := os.MkdirAll(filepath.Join(s.root, name), 0o755); err != nil {
		return nil, err
	}
	if err := writeMetadata(filepath.Join(s.root, name), metadata, "eval-run.json"); err != nil {
		return nil, err
	}
	s.generation++
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: fmt.Sprintf("%d", now.UnixNano()), name: display, dir: name, runner: configured, metadata: metadata, cancel: cancel, status: "running", started: now, byID: map[string]*TaskState{}, agents: map[string]map[message.ActorID]bool{}, changed: make(chan struct{})}
	for _, t := range tasks {
		state := &TaskState{ID: t.ID, Tier: t.Tier, Title: t.Title, Phase: string(eval.Queued)}
		j.tasks = append(j.tasks, state)
		j.byID[t.ID] = state
	}
	j.emitLocked(JobEvent{Kind: "job", Text: fmt.Sprintf("started %d tasks into %s", len(tasks), name)})
	s.jobs = append(s.jobs, j)
	go j.run(ctx, s, tasks)
	return j, nil
}

func (s *Server) findJob(id string) *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.id == id {
			return j
		}
	}
	return nil
}

// runnerInfo tells the page whether it can launch runs and with what.
type runnerInfo struct {
	Enabled       bool                            `json:"enabled"`
	Model         string                          `json:"model,omitempty"`
	Backend       string                          `json:"backend,omitempty"`
	BaseURL       string                          `json:"base_url,omitempty"`
	Profile       string                          `json:"profile,omitempty"`
	Ladder        string                          `json:"ladder,omitempty"`
	Tasks         []eval.Task                     `json:"tasks,omitempty"`
	Error         string                          `json:"error,omitempty"`
	Configuration harness.ModelConfig             `json:"configuration"`
	Roles         map[string]*harness.ModelConfig `json:"roles"`
	Flags         HarnessFlags                    `json:"flags"`
}

func (s *Server) handleRunner(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeJSON(w, runnerInfo{})
		return
	}
	info := runnerInfo{Enabled: true, Model: s.runner.Config.Model.Model, Backend: s.runner.Config.Model.Backend, BaseURL: s.runner.Config.Model.BaseURL, Profile: s.runner.Profile, Ladder: s.runner.Ladder}
	tasks, err := eval.LoadProblems(s.runner.Ladder)
	if err != nil {
		info.Error = err.Error()
	}
	for i := range tasks {
		tasks[i].Prompt, tasks[i].Insight = "", ""
	}
	info.Tasks = tasks
	info.Configuration = s.runner.Config.Model
	info.Roles = s.runner.metadata("", time.Time{}).Roles
	info.Flags = s.runner.flags()
	writeJSON(w, info)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	jobs := append([]*job(nil), s.jobs...)
	s.mu.Unlock()
	out := make([]JobSnapshot, 0, len(jobs))
	for i := len(jobs) - 1; i >= 0; i-- {
		out = append(out, jobs[i].snapshot())
	}
	writeJSON(w, map[string]any{"jobs": out})
}

func (s *Server) handleStartJob(w http.ResponseWriter, r *http.Request) {
	var body newEval
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	j, err := s.startJob(body.Tasks, body)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, j.snapshot())
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j := s.findJob(r.PathValue("id"))
	if j == nil {
		fail(w, http.StatusNotFound, errors.New("no such job"))
		return
	}
	writeJSON(w, j.snapshot())
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	j := s.findJob(r.PathValue("id"))
	if j == nil {
		fail(w, http.StatusNotFound, errors.New("no such job"))
		return
	}
	j.cancel()
	j.emit(JobEvent{Kind: "job", Text: "cancel requested"})
	writeJSON(w, j.snapshot())
}

// handleJobEvents streams progress as server-sent events: every buffered
// event past ?after, then each new one as it lands, then a final "end" once
// the job stops. Reconnecting clients resume from the last id they saw.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	j := s.findJob(r.PathValue("id"))
	if j == nil {
		fail(w, http.StatusNotFound, errors.New("no such job"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	seq := atoi(r.URL.Query().Get("after"))
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		seq = atoi(last)
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		events, changed, ended := j.after(seq)
		for _, e := range events {
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: progress\ndata: %s\n\n", e.Seq, data)
			seq = e.Seq
		}
		if len(events) > 0 {
			snapshot, _ := json.Marshal(j.snapshot())
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", snapshot)
		}
		flusher.Flush()
		if ended {
			fmt.Fprint(w, "event: end\ndata: {}\n\n")
			flusher.Flush()
			return
		}
		select {
		case <-changed:
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Default names describe the selected work; the library records the start date separately.
func defaultEvalName(model string, tasks []eval.Task) string {
	scope := fmt.Sprintf("%d problems", len(tasks))
	if len(tasks) == 1 {
		scope = tasks[0].Title
		if scope == "" {
			scope = tasks[0].ID
		}
	} else if len(tasks) > 1 {
		tier := tasks[0].Tier
		for _, task := range tasks {
			if task.Tier != tier {
				tier = ""
				break
			}
		}
		if tier != "" {
			scope = fmt.Sprintf("%d %s problems", len(tasks), tier)
		}
	}
	return model + " · " + scope
}
