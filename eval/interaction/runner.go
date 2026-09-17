package interaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

// Run creates a fresh run directory and records independent, sequential trials.
// Behavioral failures are results; invalid options and artifact write failures
// are returned as errors. Interrupted runs retain all completed trial artifacts.
func Run(ctx context.Context, opts Options) (Report, error) {
	if opts.Mode == "" {
		opts.Mode = Scripted
	}
	if opts.Mode != Scripted && opts.Mode != Live {
		return Report{}, fmt.Errorf("unknown interaction mode %q", opts.Mode)
	}
	if opts.Repetitions == 0 {
		opts.Repetitions = 1
	}
	if opts.MaxCalls == 0 {
		opts.MaxCalls = 8
	}
	if opts.MaxToolCalls == 0 {
		opts.MaxToolCalls = 24
	}
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Minute
	}
	if opts.Repetitions < 1 || opts.MaxCalls < 1 || opts.MaxToolCalls < 1 || opts.Timeout < 0 {
		return Report{}, errors.New("repetitions, call limits and timeout must be positive")
	}
	if opts.Output == "" {
		return Report{}, errors.New("interaction output directory is required")
	}
	selected := map[string]bool{}
	for _, id := range opts.ScenarioIDs {
		found := false
		for _, scenario := range List() {
			if scenario.ID == id {
				found = true
			}
		}
		if !found {
			return Report{}, fmt.Errorf("unknown interaction scenario %q", id)
		}
		selected[id] = true
	}
	if entries, err := os.ReadDir(opts.Output); err == nil && len(entries) != 0 {
		return Report{}, errors.New("interaction output directory must be empty; resume is not supported")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Report{}, err
	}
	if err := os.MkdirAll(opts.Output, 0755); err != nil {
		return Report{}, err
	}
	planned := len(selected)
	if planned == 0 {
		planned = len(List())
	}
	report := Report{Version: 1, Mode: opts.Mode, PlannedTrials: planned * opts.Repetitions}
	binaryHash, err := executableHash()
	if err != nil {
		return report, err
	}
	meta := struct {
		Report
		Commit           string `json:"commit"`
		Profile          string `json:"profile"`
		ExecutableSHA256 string `json:"executable_sha256"`
	}{report, opts.Commit, opts.Profile, binaryHash}
	if err := writeJSON(filepath.Join(opts.Output, "run.json"), meta); err != nil {
		return report, err
	}
	lines, err := os.OpenFile(filepath.Join(opts.Output, "results.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return report, err
	}
	defer lines.Close()
	for _, scenario := range List() {
		if len(selected) > 0 && !selected[scenario.ID] {
			continue
		}
		for trial := 1; trial <= opts.Repetitions; trial++ {
			if err := ctx.Err(); err != nil {
				return report, errors.Join(err, WriteReport(opts.Output, report))
			}
			result, err := runTrial(ctx, opts, scenario, trial, binaryHash)
			if err != nil {
				return report, err
			}
			report.Results = append(report.Results, result)
			if err := json.NewEncoder(lines).Encode(result); err != nil {
				return report, err
			}
			if err := lines.Sync(); err != nil {
				return report, err
			}
			if opts.Observe != nil {
				opts.Observe(result)
			}
		}
	}
	return report, WriteReport(opts.Output, report)
}

type idleProvider struct{}

func (idleProvider) Submit(ctx context.Context, _ provider.Request, _ provider.Observer) (provider.Response, error) {
	<-ctx.Done()
	return provider.Response{}, ctx.Err()
}

type requestGate struct {
	requests             chan chan struct{}
	inner                provider.Provider
	maxTools             int
	mu                   sync.Mutex
	calls, proposedTools int
	providerError        error
	outputErrors         int
	budget               bool
	beforeDispatch       func(context.Context, provider.Response) error
}

func (g *requestGate) Submit(ctx context.Context, request provider.Request, observer provider.Observer) (provider.Response, error) {
	permit := make(chan struct{})
	select {
	case g.requests <- permit:
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
	select {
	case <-permit:
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	r, err := g.inner.Submit(ctx, request, observer)
	g.mu.Lock()
	if err != nil && ctx.Err() == nil {
		var malformed *provider.ToolArgumentsError
		if errors.As(err, &malformed) || errors.Is(err, agent.ErrReasoningLimit) {
			g.outputErrors++
		} else {
			g.providerError = err
		}
	}
	if err == nil {
		g.proposedTools += len(r.ToolCalls)
		if g.proposedTools > g.maxTools {
			g.budget = true
			g.mu.Unlock()
			return provider.Response{}, errors.New("interaction tool-call budget exceeded before dispatch")
		}
	}
	g.mu.Unlock()
	if err == nil && ctx.Err() == nil && g.beforeDispatch != nil {
		if e := g.beforeDispatch(ctx, r); e != nil {
			return provider.Response{}, e
		}
	}
	return r, err
}

func (g *requestGate) stats() (int, int, error, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls, g.outputErrors, g.providerError, g.budget
}

type boundary struct {
	cursor eventlog.Cursor
	reason string
	err    error
}

func runTrial(ctx context.Context, opts Options, scenario Scenario, trial int, binaryHash string) (result Result, retErr error) {
	dir := filepath.Join(opts.Output, scenario.ID, fmt.Sprintf("%03d", trial))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return result, err
	}
	result = Result{ScenarioID: scenario.ID, Trial: trial, Mode: opts.Mode, Outcome: "error", StartedAt: time.Now(),
		Trace: filepath.Join(scenario.ID, fmt.Sprintf("%03d", trial), "trace.jsonl"), Manifest: filepath.Join(scenario.ID, fmt.Sprintf("%03d", trial), "manifest.json")}
	if schemaRole(scenario.ID) != "" {
		// Setup/provider failures remain visible in schema trial denominators.
		result.Schema = &SchemaBehavior{}
	}
	defer func() {
		result.Duration = time.Since(result.StartedAt)
		retErr = errors.Join(retErr, writeJSON(filepath.Join(dir, "result.json"), result))
	}()
	trialCtx, cancelTrial := context.WithTimeout(ctx, opts.Timeout)
	defer cancelTrial()
	cfg := opts.Config
	if cfg.Root.Prompt.Role == "" {
		cfg = harness.DefaultConfig()
	}
	workspace, err := os.MkdirTemp("", "strap-interaction-")
	if err != nil {
		result.ErrorClass = "setup"
		result.Error = err.Error()
		return result, nil
	}
	defer os.RemoveAll(workspace)
	cfg.Dir, cfg.LocalTools, cfg.Web = workspace, false, nil
	cfg.ResearchExecution.Enabled = false
	cfg.Telemetry.ContextTokens = false
	cfg.Events.JSONLPath = filepath.Join(dir, "trace.jsonl")
	gate := &requestGate{requests: make(chan chan struct{}, 1), maxTools: opts.MaxToolCalls}
	role := schemaRole(scenario.ID)
	if role == "" {
		role = roster.Root
	}
	deps := harness.Dependencies{Provider: idleProvider{}}
	switch role {
	case roster.Root:
		deps.Root.Provider = gate
	case roster.Implementor:
		deps.Implementor.Provider = gate
	case roster.Auditor:
		deps.Auditor.Provider = gate
	default:
		result.ErrorClass, result.Error = "setup", "unsupported evaluated actor role"
		return result, nil
	}
	// The owner context is independent of the trial deadline: freeze the grading
	// prefix before cleanup introduces cancellation and session-close records.
	s, err := harness.New(context.Background(), cfg, deps)
	if err != nil {
		result.ErrorClass = "setup"
		result.Error = err.Error()
		var startup *harness.StartupError
		if errors.As(err, &startup) {
			_ = startup.Close(context.Background())
		}
		return result, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Dispose(cleanup); err != nil {
			result.Outcome = "error"
			result.Behavior.Scorable = false
			result.ErrorClass = "cleanup"
			result.Error = err.Error()
		}
	}()
	if err := pauseRoot(trialCtx, s); err != nil {
		result.ErrorClass = "setup"
		result.Error = err.Error()
		return result, nil
	}
	var f fixture
	if schemaRole(scenario.ID) != "" {
		f, err = seedSchema(trialCtx, s, scenario.ID)
	} else {
		f, err = seedAudit(trialCtx, s)
	}
	if err != nil {
		result.ErrorClass = "setup"
		result.Error = err.Error()
		return result, nil
	}
	if f.actor() != s.Root() {
		if err := pauseActor(trialCtx, s, f.actor()); err != nil {
			result.ErrorClass, result.Error = "setup", err.Error()
			return result, nil
		}
	}
	actorConfig := effectiveRole(s.Configuration(), role)
	if f.Schema != nil {
		f.Schema.Tools = actorConfig.Tools
	}
	reader, err := s.Trace(trialCtx)
	if err != nil {
		result.ErrorClass = "capture"
		result.Error = err.Error()
		return result, nil
	}
	defer reader.Close(context.Background())
	head, err := reader.Head(trialCtx)
	if err != nil {
		result.ErrorClass = "capture"
		result.Error = err.Error()
		return result, nil
	}
	result.Start = head.Cursor
	var race *revisionRace
	if scenario.ID == "audit-revision-race" {
		race = &revisionRace{session: s, reader: reader, fixture: f}
		gate.beforeDispatch = race.inject
	}
	if opts.Mode == Scripted {
		gate.inner = f.script(scenario.ID)
	} else if opts.Provider != nil {
		gate.inner = opts.Provider
	} else {
		model := roleModel(cfg, role)
		gate.inner, err = model.NewProvider(nil)
		if err != nil {
			result.ErrorClass = "provider"
			result.Error = err.Error()
			return result, nil
		}
	}
	stimulus := f.stimulus(scenario.ID)
	manifest := struct {
		Scenario         Scenario                `json:"scenario"`
		Mode             Mode                    `json:"mode"`
		Fixture          fixture                 `json:"fixture"`
		Stimulus         string                  `json:"stimulus"`
		Start            eventlog.Cursor         `json:"start"`
		Config           harness.EffectiveConfig `json:"config"`
		Model            harness.ModelConfig     `json:"model"`
		OpaqueProvider   bool                    `json:"opaque_provider"`
		MaxCalls         int                     `json:"max_calls"`
		MaxToolCalls     int                     `json:"max_tool_calls"`
		Timeout          time.Duration           `json:"timeout_ns"`
		ExecutableSHA256 string                  `json:"executable_sha256"`
		PromptSHA256     string                  `json:"prompt_sha256"`
		ToolsSHA256      string                  `json:"tools_sha256"`
	}{scenario, opts.Mode, f, stimulus, result.Start, s.Configuration(), safeModel(cfg, role), opts.Provider != nil, opts.MaxCalls, opts.MaxToolCalls, opts.Timeout, binaryHash, "", ""}
	manifest.PromptSHA256 = hashJSON(actorConfig.Prompt)
	manifest.ToolsSHA256 = hashJSON(actorConfig.Tools)
	if err := writeJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return result, err
	}
	sub, err := s.Subscribe(trialCtx, harness.SubscribeOptions{After: result.Start})
	if err != nil {
		result.ErrorClass = "capture"
		result.Error = err.Error()
		return result, nil
	}
	defer sub.Close()
	boundaries := make(chan boundary)
	watchCtx, stopWatch := context.WithCancel(trialCtx)
	defer stopWatch()
	go func() {
		for {
			rec, err := sub.Next(watchCtx)
			if err != nil {
				select {
				case boundaries <- boundary{err: err}:
				case <-watchCtx.Done():
				}
				return
			}
			if rec.Agent != string(f.actor()) {
				continue
			}
			reason := ""
			switch rec.Kind {
			case "tool_batch":
				reason = "batch"
			case "agent_yielded":
				reason = "yield"
			case "agent_exited":
				reason = "agent_exit"
			case "message":
				e, eerr := s.ResolveRecord(watchCtx, rec)
				if eerr != nil {
					select {
					case boundaries <- boundary{err: eerr}:
					case <-watchCtx.Done():
					}
					return
				}
				fact, eerr := eventcodec.DecodeEvent(e)
				if eerr != nil {
					select {
					case boundaries <- boundary{err: eerr}:
					case <-watchCtx.Done():
					}
					return
				}
				m, ok := fact.(conversation.MessageEvent)
				if ok && m.Message.From == f.actor() && m.Message.Kind == message.Reply {
					reason = "reply"
				}
			}
			if reason != "" {
				select {
				case boundaries <- boundary{cursor: rec.Cursor(), reason: reason}:
				case <-watchCtx.Done():
					return
				}
			}
		}
	}()
	if _, err = s.Send(f.actor(), stimulus); err == nil {
		_, err = s.ResumeAgent(f.actor())
	}
	if err != nil {
		result.ErrorClass = "harness"
		result.Error = err.Error()
		return result, nil
	}
	var facts []fact
	observationFailed := func(err error) {
		if trialCtx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			result.Through = eventlog.Cursor{}
			result.StopReason = "timeout"
			if ctx.Err() != nil {
				result.ErrorClass = "interrupted"
				result.Error = ctx.Err().Error()
			}
			return
		}
		result.ErrorClass = "capture"
		result.Error = err.Error()
	}
	running := true
	for running {
		select {
		case permit := <-gate.requests:
			h, e := reader.Head(trialCtx)
			if e == nil {
				facts, e = readFacts(trialCtx, reader, h.Cursor)
			}
			if e != nil {
				observationFailed(e)
				running = false
				break
			}
			calls, _, _, _ := gate.stats()
			if b, ok := firstBoundary(facts, result.Start, f); ok {
				result.Through = b.cursor
				result.StopReason = b.reason
				facts, e = readFacts(trialCtx, reader, b.cursor)
				if e != nil {
					observationFailed(e)
				}
				running = false
			} else if calls >= opts.MaxCalls {
				result.Through = h.Cursor
				result.StopReason = "call_budget"
				running = false
			} else {
				close(permit)
			}
		case b := <-boundaries:
			if b.err != nil {
				observationFailed(b.err)
				running = false
				break
			}
			observed, e := readFacts(trialCtx, reader, b.cursor)
			if e != nil {
				observationFailed(e)
				running = false
				break
			}
			if terminal, ok := firstBoundary(observed, result.Start, f); ok {
				facts = observed
				result.Through = terminal.cursor
				result.StopReason = terminal.reason
				if terminal.cursor != b.cursor {
					facts, e = readFacts(trialCtx, reader, terminal.cursor)
					if e != nil {
						observationFailed(e)
					}
				}
				running = false
			}
		case <-trialCtx.Done():
			result.StopReason = "timeout"
			if ctx.Err() != nil {
				result.ErrorClass = "interrupted"
				result.Error = ctx.Err().Error()
			}
			running = false
		}
	}
	// No new model response can pass the gate once the observation loop ends.
	if result.Through.Sequence == 0 {
		inspectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		h, e := reader.Head(inspectCtx)
		if e == nil {
			result.Through = h.Cursor
			facts, e = readFacts(inspectCtx, reader, h.Cursor)
		}
		cancel()
		if e != nil && result.Error == "" {
			result.ErrorClass = "capture"
			result.Error = e.Error()
		}
	}
	result.ModelCalls, result.Behavior.OutputErrors, err, _ = gate.stats()
	_, _, _, toolBudget := gate.stats()
	if err != nil && result.Error == "" {
		result.ErrorClass = "provider"
		result.Error = err.Error()
	}
	if toolBudget {
		result.StopReason = "tool_budget"
	}
	if race != nil {
		var raceErr error
		result.RevisionRace, raceErr = race.snapshot()
		if raceErr != nil {
			result.ErrorClass = "intervention"
			result.Error = raceErr.Error()
		}
	}
	if result.Error == "" {
		grade(&result, scenario, f, facts)
		if result.StopReason == "timeout" || result.StopReason == "call_budget" || result.StopReason == "tool_budget" {
			result.Outcome = "failed"
			result.ErrorClass = "budget"
			result.Error = "scenario did not finish within its declared budget"
			result.Behavior.OutcomeCorrect = false
			result.Behavior.CleanSuccess = false
			result.Behavior.RecoverySuccess = false
		}
	}
	// Seal the archive before reopening it, but compare the pre-cleanup prefix.
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = s.Close(cleanup)
	cancel()
	if err != nil && result.Error == "" {
		result.ErrorClass = "cleanup"
		result.Error = err.Error()
		result.Outcome = "error"
	}
	if result.ErrorClass == "" || result.ErrorClass == "budget" {
		archive, err := inspection.OpenJSONL(context.Background(), cfg.Events.JSONLPath)
		if err == nil {
			var replay []fact
			replay, err = readFacts(context.Background(), archive, result.Through)
			if err == nil {
				addReplayAssertion(&result, f, facts, replay)
			}
			_ = archive.Close(context.Background())
		}
		if err != nil {
			result.ErrorClass = "capture"
			result.Error = err.Error()
			result.Outcome = "error"
		}
	}
	if result.ErrorClass != "" && result.ErrorClass != "budget" {
		result.Outcome = "error"
		result.Behavior.Scorable = false
	}
	return result, nil
}

func pauseRoot(ctx context.Context, s *harness.Session) error {
	return pauseActor(ctx, s, s.Root())
}

func pauseActor(ctx context.Context, s *harness.Session, actor identity.ActorID) error {
	sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		return err
	}
	defer sub.Close()
	info, err := s.PauseAgent(actor)
	if err != nil {
		return err
	}
	if info.State == agent.Paused {
		return nil
	}
	for {
		rec, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if rec.Kind != "agent_state" || rec.Agent != string(actor) {
			continue
		}
		rec, err = s.ResolveRecord(ctx, rec)
		if err != nil {
			return err
		}
		e, err := eventcodec.DecodeEvent(rec)
		if err != nil {
			return err
		}
		if state, ok := e.(conversation.AgentStateChanged); ok && state.State == agent.Paused {
			return nil
		}
	}
}

func roleModel(cfg harness.Config, role roster.Role) harness.ModelConfig {
	m := cfg.Model
	selected := cfg.Root
	switch role {
	case roster.Implementor:
		selected = cfg.Implementor
	case roster.Auditor:
		selected = cfg.Auditor
	}
	if selected.Model != nil {
		m = *selected.Model
	}
	return m
}

func effectiveRole(cfg harness.EffectiveConfig, role roster.Role) harness.RoleConfiguration {
	switch role {
	case roster.Implementor:
		return cfg.Implementor
	case roster.Auditor:
		return cfg.Auditor
	default:
		return cfg.Root
	}
}

func safeModel(cfg harness.Config, role roster.Role) harness.ModelConfig {
	m := roleModel(cfg, role)
	if u, err := url.Parse(m.BaseURL); err == nil {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		m.BaseURL = u.String()
	}
	return m
}
func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func hashJSON(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func executableHash() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
