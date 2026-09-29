package replay

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/tool"
)

// Options tune a replay.
type Options struct {
	// Trace is where the replay writes its own trace, which can be replayed
	// and checked like any other. Empty keeps it in memory.
	Trace string
	// Stall is how long the replay waits for a recorded point before it
	// reports the point as never reached and moves past it. Default 5s.
	Stall time.Duration
	// Settle is how long the session must stay quiet after the last recorded
	// point before the replay ends. Default 1s.
	Settle time.Duration
	// Configure adjusts the rebuilt configuration, such as a candidate prompt.
	Configure func(*harness.Config)

	capture *target // Probes stop the replay at one call and take its request.
}

// Result is what a replay found.
type Result struct {
	Divergences []Divergence
	Served      int // Recorded model outputs the replay asked for.
	Recorded    int // Recorded model outputs.
	Notes       []string
}

// Faithful reports whether the replay made the recorded requests, and only
// those, and served every recorded output.
func (r Result) Faithful() bool { return len(r.Divergences) == 0 && r.Served == r.Recorded }

// Config rebuilds the session configuration a recording ran with. Traces
// made before the configuration recorded local and web tools have them
// inferred from the tools their roles were given.
func Config(rec *Recording) harness.Config {
	c := rec.Config
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.ReasoningLimit, cfg.Seed = c.Dir, c.ReasoningLimit, slices.Clone(rec.Seed)
	cfg.LocalTools, cfg.FileEdits, cfg.Web, cfg.DebugToolkit = c.LocalTools, c.FileEdits, c.Web, c.DebugToolkit
	var names []string
	for _, role := range c.Roles() {
		for _, t := range role.Tools {
			names = append(names, t.Name)
		}
	}
	if slices.Contains(names, "read_file") {
		cfg.LocalTools = true
	}
	if cfg.Web == nil && (slices.Contains(names, "web_search") || slices.Contains(names, "open_url")) {
		cfg.Web = &tool.WebConfig{}
	}
	cfg.DeepResearch, cfg.WorkProgressReporting = c.DeepResearch, c.WorkProgressReporting
	cfg.Telemetry = c.Telemetry
	cfg.Telemetry.ContextTokens = false // Measures with the model server; nothing a model sees.
	cfg.Telemetry.ServerMetrics = false
	if c.LSP == nil {
		cfg.LSP = nil
	}
	if c.Agent != nil {
		cfg.Solo, cfg.Tester, cfg.Agent.Prompt = true, c.Tester, c.Agent.Prompt.Clone()
	}
	cfg.Manager.Prompt = c.Manager.Prompt.Clone()
	cfg.Implementor.Prompt, cfg.Auditor.Prompt, cfg.WebResearcher.Prompt = c.Implementor.Prompt.Clone(), c.Auditor.Prompt.Clone(), c.WebResearcher.Prompt.Clone()
	if c.DeepResearcher != nil {
		cfg.DeepResearcher.Prompt = c.DeepResearcher.Prompt.Clone()
	}
	if c.Experimenter != nil {
		cfg.Experimenter.Prompt = c.Experimenter.Prompt.Clone()
	}
	if c.Reviewer != nil {
		cfg.Reviewer.Prompt = c.Reviewer.Prompt.Clone()
	}
	if c.Debugger != nil {
		cfg.Debugger.Prompt = c.Debugger.Prompt.Clone()
	}
	cfg.Events = harness.EventConfig{Queue: c.Events.Queue}
	return cfg
}

// replaying is a session running a recording.
type replaying struct {
	p     *player
	s     *harness.Session
	notes []string
	stop  func()
}

// start builds the replay session; the caller must call stop.
func start(ctx context.Context, rec *Recording, opts Options) (*replaying, error) {
	if opts.Stall <= 0 {
		opts.Stall = 5 * time.Second
	}
	cfg := Config(rec)
	cfg.Events.JSONLPath = opts.Trace
	if opts.Configure != nil {
		opts.Configure(&cfg)
	}
	r := &replaying{stop: func() {}}
	if len(rec.Seed) == 0 {
		r.notes = append(r.notes, "the trace records no seed, so the replay issues different work ids")
	}
	var cleanup []func()
	if _, err := os.Stat(cfg.Dir); err != nil {
		// The recorded workspace is gone, typically a container's. Every file
		// the model saw is served from the recording, so any directory will
		// do; a request that still shows the difference is a divergence.
		dir, err := os.MkdirTemp("", "strap-replay-")
		if err != nil {
			return nil, err
		}
		cleanup = append(cleanup, func() { os.RemoveAll(dir) })
		r.notes = append(r.notes, fmt.Sprintf("the recorded workspace %s does not exist here; the replay ran in %s", cfg.Dir, dir))
		cfg.Dir = dir
	}
	r.p = newPlayer(rec, opts.Stall)
	r.p.target = opts.capture
	if cfg.Dir != rec.Config.Dir {
		r.p.dir = cfg.Dir
	}
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: r.p, TesterRuns: r.p.testerRun, Environment: r.p.environment, Workspace: r.p.workspace, Intake: r.p.intake, SendOrder: r.p.sendOrder, Sequence: r.p.sequence, Clock: r.p.clock})
	if err != nil {
		for _, f := range cleanup {
			f()
		}
		return nil, err
	}
	r.s = s
	r.stop = func() {
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = s.Close(shutdown)
		_ = s.Dispose(shutdown)
		for _, f := range cleanup {
			f()
		}
	}
	return r, nil
}

// sendUsers sends each recorded user message at its recorded point.
func (r *replaying) sendUsers(ctx context.Context) error {
	for i, u := range r.p.rec.Users {
		key := userGate(i)
		if err := r.p.await(ctx, key); err != nil {
			return err
		}
		if _, err := r.s.Send(u.To, u.Text); err != nil {
			return fmt.Errorf("send user message %d: %w", i+1, err)
		}
		r.p.pass(key)
	}
	return nil
}

// Run replays a recording in a fresh session.
func Run(ctx context.Context, rec *Recording, opts Options) (Result, error) {
	if opts.Settle <= 0 {
		opts.Settle = time.Second
	}
	if opts.Stall <= 0 {
		opts.Stall = 5 * time.Second
	}
	r, err := start(ctx, rec, opts)
	if err != nil {
		return Result{}, err
	}
	defer r.stop()
	p, s, notes := r.p, r.s, r.notes
	if err := r.sendUsers(ctx); err != nil {
		return Result{}, err
	}
	for {
		p.mu.Lock()
		quiet := time.Since(p.progress)
		p.mu.Unlock()
		if p.done() && quiet >= opts.Settle {
			break
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		// A replay that stops calling the model never reaches its remaining
		// points; await reports and skips them after the stall period.
		if !p.done() && quiet >= opts.Stall {
			_ = p.await(ctx, "end of recording")
		}
	}
	p.mu.Lock()
	result := Result{Divergences: slices.Clone(p.divergences), Served: p.served, Recorded: len(rec.Outputs), Notes: notes}
	p.mu.Unlock()
	agents := s.Agents()
	for i, want := range rec.Registrations {
		if i >= len(agents) {
			result.Divergences = append(result.Divergences, Divergence{Kind: "topology", Detail: fmt.Sprintf("%s (%s) was never created", want.AgentID, want.Role)})
			continue
		}
		if got := agents[i]; got.ID != want.AgentID || got.Role != want.Role || got.Parent != want.Parent {
			result.Divergences = append(result.Divergences, Divergence{Kind: "topology", Detail: fmt.Sprintf("agent %d is %s (%s under %s); recorded %s (%s under %s)", i+1, got.ID, got.Role, got.Parent, want.AgentID, want.Role, want.Parent)})
		}
	}
	for _, extra := range agents[min(len(agents), len(rec.Registrations)):] {
		result.Divergences = append(result.Divergences, Divergence{Kind: "topology", Detail: fmt.Sprintf("%s (%s) was created; the recording has no such agent", extra.ID, extra.Role)})
	}
	return result, nil
}
