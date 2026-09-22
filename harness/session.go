// Package harness assembles one independently running root and its audited work.
// Terminal and transport adapters do not select execution policy.
package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/admission"
	"github.com/stevemurr/strap/internal/resource"
	"github.com/stevemurr/strap/internal/transport"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/research"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type Resource = resource.Resource

type AgentConfig struct {
	Prompt prompt.Prompt `json:"prompt"`
	Model  *ModelConfig  `json:"model,omitempty"` // Nil uses the session model configuration.
}

type WorkProgressReportingConfig = workflow.WorkProgressReportingConfig

type ResearchExecutionConfig struct {
	Enabled     bool          `json:"enabled"`
	Timeout     time.Duration `json:"timeout"`
	MaxTimeout  time.Duration `json:"max_timeout"`
	OutputLimit int           `json:"output_limit"`
	Env         []string      `json:"env"`
}
type Config struct {
	DeepResearch          DeepResearchConfig          `json:"deep_research"`
	LSP                   *lsp.Config                 `json:"lsp,omitempty"` // Nil disables experimental language tools.
	ResearchExecution     ResearchExecutionConfig     `json:"research_execution"`
	WorkProgressReporting WorkProgressReportingConfig `json:"work_progress_reporting"`
	Telemetry             TelemetryConfig             `json:"telemetry"`
	Events                EventConfig                 `json:"events"`
	Dir                   string                      `json:"dir"`
	ReasoningLimit        int                         `json:"reasoning_limit"` // Reasoning bytes per model call; zero is unlimited.
	Model                 ModelConfig                 `json:"model"`
	LocalTools            bool                        `json:"local_tools"`
	Web                   *tool.WebConfig             `json:"web"` // Nil disables browser/search tools.
	Root                  AgentConfig                 `json:"root"`
	Implementor           AgentConfig                 `json:"implementor"`
	Auditor               AgentConfig                 `json:"auditor"`
	Researcher            AgentConfig                 `json:"researcher"`
}

// DefaultConfig returns independent library defaults without acquiring resources.
// The CLI selects its model from its own model catalog.
func DefaultConfig() Config {
	languages := lsp.DefaultConfig()
	return Config{ResearchExecution: ResearchExecutionConfig{Enabled: true, Timeout: 30 * time.Second, MaxTimeout: 60 * time.Second, OutputLimit: 16 * 1024}, WorkProgressReporting: workflow.DefaultWorkProgressReporting(), Telemetry: TelemetryConfig{ContextTokens: true, Concurrency: 2, Queue: 128, Timeout: 10 * time.Second}, Events: EventConfig{Queue: eventlog.Limits{Entries: 1024, Bytes: 8 << 20}}, Dir: ".", ReasoningLimit: 192 << 10, Model: ModelConfig{Backend: "vllm", BaseURL: "http://127.0.0.1:8000", Model: "qwen3.6", Timeout: 60 * time.Minute}, LocalTools: true, Web: &tool.WebConfig{}, LSP: &languages,
		Root: AgentConfig{Prompt: rootPrompt.Clone()}, Implementor: AgentConfig{Prompt: executionPrompt.Clone()}, Auditor: AgentConfig{Prompt: auditorPrompt.Clone()}, Researcher: AgentConfig{Prompt: researcherPrompt.Clone()}}
}

type EventConfig struct {
	JSONLPath string          `json:"jsonl_path,omitempty"` // Empty uses memory; nonempty exclusively creates a durable trace file.
	Retention eventlog.Limits `json:"retention"`            // Optional hard memory-store quota; zero retains without a quota.
	Queue     eventlog.Limits `json:"queue"`
}

type AgentDependencies struct {
	Provider provider.Provider
	Tools    []tool.Tool // Additional borrowed tools for this role.
}

type OwnedResource struct {
	Name     string
	Resource Resource
}

// Dependencies are executable collaborators. Providers/tools are borrowed unless
// also registered in Resources. Resource ownership transfers when New is called,
// including when construction fails and returns a cleanup handle.
type Dependencies struct {
	DeepResearchProvider                   provider.Provider
	ResearchWeb                            research.Retrieval
	LSP                                    lsp.Dependencies
	CaptureFailure                         func(error)       // Optional independent diagnostic sink; must return promptly.
	Provider                               provider.Provider // Shared fallback for all roles, useful for eval fakes.
	Root, Implementor, Auditor, Researcher AgentDependencies
	EventStore                             func(sessionID string) (eventlog.Store, error) // Factory transfers storage ownership; called once.
	Resources                              []OwnedResource
}

type Session struct {
	researchReads    *inspection.ResearchReader
	interactionMu    sync.Mutex
	interruption     *interruptAttempt
	progressReads    *inspection.ProgressReader
	hostMessage      atomic.Uint64
	encoder          *eventcodec.Publisher
	projectionGate   chan struct{}
	projection       *projection.Projector
	projectionError  error
	workflowAfter    uint64
	workflowReadLife context.Context
	executionError   error
	outcome          *eventlog.Outcome
	startupError     string
	effective        EffectiveConfig
	telemetry        *telemetry
	id               string
	log              *eventlog.Log
	legacyOnce       sync.Once
	legacy           *eventlog.Subscription
	config           Config
	controller       *conversation.Controller
	workflow         *workflow.Session
	resources        *resource.Group
	mu               sync.Mutex
	state            State
	attempt          *closeAttempt
	admission        *admission.Gate
	cancelExecution  context.CancelFunc
	stopOwner        func() bool
}

// StartupError retains cleanup ownership if rollback could not complete.
// Call Close again through errors.As; a partially built session is never usable.
type startupCleanup struct{ s *Session }

func (c startupCleanup) Close(ctx context.Context) error { return c.s.Dispose(ctx) }

type StartupError struct {
	cause   error
	cleanup Resource
}

func (e *StartupError) Error() string                   { return e.cause.Error() }
func (e *StartupError) Unwrap() error                   { return e.cause }
func (e *StartupError) Close(ctx context.Context) error { return e.cleanup.Close(ctx) }

func New(ctx context.Context, cfg Config, deps Dependencies) (_ *Session, err error) {
	cfg = cloneConfig(cfg)
	for _, role := range []*AgentConfig{&cfg.Root, &cfg.Implementor, &cfg.Auditor, &cfg.Researcher} {
		if !slices.Contains(role.Prompt.Instructions, fileInstruction) {
			role.Prompt.Instructions = append(role.Prompt.Instructions, fileInstruction)
		}
		if cfg.LSP != nil && !slices.Contains(role.Prompt.Instructions, languageInstruction) {
			role.Prompt.Instructions = append(role.Prompt.Instructions, languageInstruction)
		}
	}
	execution, cancelExecution := context.WithCancel(context.WithoutCancel(ctx))
	s := &Session{projectionGate: make(chan struct{}, 1), config: cloneConfig(cfg), resources: resource.New(), state: Open, cancelExecution: cancelExecution, admission: admission.New(execution)}
	defer func() {
		if err == nil {
			return
		}
		s.mu.Lock()
		s.startupError = err.Error()
		s.mu.Unlock()
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cleanupErr := s.Dispose(cleanup); cleanupErr != nil {
			err = &StartupError{cause: errors.Join(err, cleanupErr), cleanup: startupCleanup{s}}
		}
	}()
	for _, r := range deps.Resources {
		if r.Resource == nil {
			err = errors.Join(err, fmt.Errorf("resource %q is nil", r.Name))
			continue
		}
		s.resources.Add(r.Name, r.Resource)
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.ResearchExecution.Enabled && (cfg.ResearchExecution.Timeout < time.Millisecond || cfg.ResearchExecution.MaxTimeout < cfg.ResearchExecution.Timeout || cfg.ResearchExecution.OutputLimit < 2) {
		return nil, errors.New("invalid research execution limits")
	}
	if err = cfg.WorkProgressReporting.Validate(); err != nil {
		return nil, err
	}
	if cfg.ReasoningLimit < 0 {
		return nil, errors.New("reasoning limit must not be negative")
	}
	if cfg.DeepResearch.Enabled {
		cfg.DeepResearch.Limits, err = cfg.DeepResearch.Limits.Resolve()
		if err != nil {
			return nil, err
		}
		if cfg.Web == nil && deps.ResearchWeb == nil {
			return nil, errors.New("deep research requires web retrieval")
		}
		cfg.Researcher.Prompt.Instructions = append(cfg.Researcher.Prompt.Instructions, "For a multi-source investigation, use deep_research with your active work_id and explicit success criteria. Read its report and source evidence with get_research_report. Forward selected verified findings using report_work_progress before submit_research; report claim IDs are not ledger finding IDs. State partial outcomes and remaining gaps.")
		cfg.Root.Prompt.Instructions = append(cfg.Root.Prompt.Instructions, "Researchers have an opt-in deep_research tool for bounded multi-source web investigations. Assign a researcher a clear question and acceptance criteria. Read retained runs through get_research_report. You remain available while research executes; send_message does not steer an in-flight investigation.")
	}
	s.config.DeepResearch = cfg.DeepResearch
	s.config.Root = cfg.Root
	s.config.Researcher = cfg.Researcher
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	s.id = hex.EncodeToString(id[:])
	s.projection = projection.New(identity.SessionID(s.id))
	if err = s.config.Telemetry.defaults(); err != nil {
		return nil, err
	}
	if s.config.Events.JSONLPath != "" && deps.EventStore != nil {
		return nil, errors.New("configure JSONLPath or EventStore, not both")
	}
	if s.config.Events.Queue == (eventlog.Limits{}) {
		s.config.Events.Queue = DefaultConfig().Events.Queue
	}
	if s.config.Events.Retention != (eventlog.Limits{}) {
		if err = s.config.Events.Retention.Validate(); err != nil {
			return nil, err
		}
	}
	if err = s.config.Events.Queue.Validate(); err != nil {
		return nil, err
	}
	var store eventlog.Store
	if deps.EventStore != nil {
		store, err = deps.EventStore(s.id)
		if err == nil && store == nil {
			err = errors.New("event store factory returned nil")
		}
		if err != nil {
			if store != nil {
				s.resources.Add("event store", store)
			}
			return nil, err
		}
	}
	if store == nil {
		if s.config.Events.JSONLPath != "" {
			store, err = eventlog.NewJSONL(s.config.Events.JSONLPath, s.id)
		} else {
			store, err = eventlog.NewMemory(s.id, s.config.Events.Retention)
		}
		if err != nil {
			return nil, err
		}
	}
	head, headErr := store.Head(ctx)
	if headErr != nil || head.Cursor.Session != s.id || head.Cursor.Sequence != 0 || head.State != eventlog.Writable {
		s.resources.Add("invalid event store", store)
		return nil, errors.Join(headErr, errors.New("event store must be empty, writable, and bound to this session"))
	}
	s.log, err = eventlog.New(store, s.config.Events.Queue, eventlog.WithFailureSink(func(err error) {
		s.cancelExecution()
		if deps.CaptureFailure != nil {
			deps.CaptureFailure(err)
		}
	}))
	if err != nil {
		return nil, err
	}
	s.encoder = eventcodec.NewPublisher(s.log, s.config.Events.Queue.Bytes)
	data, _ := json.Marshal(struct {
		ID string `json:"id"`
	}{s.id})
	if err = s.log.Publish(eventlog.Data{Kind: "session_started", Payload: data}); err != nil {
		return nil, err
	}
	pool := transport.New()
	s.resources.Add("provider transport", pool)
	cfg = s.config
	injected := func(d AgentDependencies) bool { return d.Provider != nil || deps.Provider != nil }
	makeProvider := func(role AgentConfig, d AgentDependencies) (provider.Provider, error) {
		if d.Provider != nil {
			return d.Provider, nil
		}
		if deps.Provider != nil {
			return deps.Provider, nil
		}
		model := cfg.roleModel(role)
		if model.Timeout <= 0 {
			return nil, errors.New("model timeout must be positive")
		}
		return model.NewProvider(&http.Client{Transport: pool, Timeout: model.Timeout})
	}
	root, err := makeProvider(cfg.Root, deps.Root)
	if err != nil {
		return nil, err
	}
	implementor, err := makeProvider(cfg.Implementor, deps.Implementor)
	if err != nil {
		return nil, err
	}
	auditor, err := makeProvider(cfg.Auditor, deps.Auditor)
	if err != nil {
		return nil, err
	}
	researcher, err := makeProvider(cfg.Researcher, deps.Researcher)
	if err != nil {
		return nil, err
	}
	var languages *lsp.Manager
	var languageTools []tool.Tool
	var changed func(string)
	var afterRun func()
	if cfg.LSP != nil {
		languageConfig := cfg.LSP.Clone()
		languageConfig.Dir = cfg.Dir
		languages, err = lsp.New(languageConfig, deps.LSP)
		if err != nil {
			return nil, err
		}
		s.resources.Add("lsp", languages)
		languageTools, err = tool.LSPTools(languages)
		if err != nil {
			return nil, err
		}
		changed = languages.Changed
		afterRun = func() { languages.Changed("") }
	}
	var webRuntime *tool.Web
	var local []tool.Tool
	if cfg.LocalTools {
		local, err = localToolsWithChanges(cfg.Dir, changed, afterRun)
		if err != nil {
			return nil, err
		}
	}
	local = append(local, languageTools...)
	if cfg.Web != nil {
		web, e := tool.NewWeb(*cfg.Web)
		if e != nil {
			return nil, e
		}
		webRuntime = web
		s.resources.Add("web", web)
		local = append(local, web.Tools()...)
	}
	var feedback *languageFeedback
	if languages != nil {
		feedback = &languageFeedback{manager: languages, seen: map[identity.ActorID]string{}}
		for i, t := range local {
			local[i] = feedback.wrap(t)
		}
	}
	c := conversation.New(execution, conversation.WithInboxAdmission(s.inboxAdmission), conversation.WithWakeContext(s.wakeContext), conversation.WithReporting(conversation.ReporterFunc(func(_ context.Context, e conversation.Event) error { return s.publish(e) }), s.readWorkflow))
	s.controller = c
	var stopReads context.CancelFunc
	s.workflowReadLife, stopReads = context.WithCancel(context.Background())
	go func() { <-c.Done(); stopReads() }()
	s.telemetry = newTelemetry(s)
	readSource, readErr := inspection.New(ctx, s.log)
	if readErr != nil {
		return nil, readErr
	}
	s.resources.Add("progress reader", readSource)
	s.progressReads, err = inspection.NewProgressReader(readSource)
	if err != nil {
		return nil, err
	}
	s.progressReads.Evidence = inspection.ReadExecutionEvidence
	s.progressReads.Authorize = func(ctx context.Context, actor identity.ActorID, id work.ID) error {
		_, err := s.GetWork(ctx, actor, id)
		return err
	}
	messaging := []tool.Tool{tool.SendMessage(), tool.MessageStatus(c.Receipt)}
	withoutWrites := slices.DeleteFunc(slices.Clone(local), func(t tool.Tool) bool { n := t.Definition().Name; return n == "write_file" || n == "edit_file" })
	researchReads := slices.DeleteFunc(slices.Clone(withoutWrites), func(t tool.Tool) bool { return t.Definition().Name == "shell" })
	var researchShell tool.Tool
	if cfg.LocalTools && cfg.ResearchExecution.Enabled {
		researchShell, err = tool.NewShell(tool.ShellConfig{Dir: cfg.Dir, AfterRun: afterRun, Timeout: cfg.ResearchExecution.Timeout, MaxTimeout: cfg.ResearchExecution.MaxTimeout, OutputLimit: cfg.ResearchExecution.OutputLimit, Env: cfg.ResearchExecution.Env})
		if err != nil {
			return nil, err
		}
	}
	if feedback != nil && researchShell != nil {
		researchShell = feedback.wrap(researchShell)
	}
	var deepOption workflow.Option = func(*workflow.Session) {}
	if cfg.DeepResearch.Enabled {
		model := researcher
		if deps.DeepResearchProvider != nil {
			model = deps.DeepResearchProvider
		} else if cfg.DeepResearch.Model != nil {
			model, err = makeProvider(AgentConfig{Model: cfg.DeepResearch.Model}, AgentDependencies{})
			if err != nil {
				return nil, err
			}
		}
		engine, e := research.New(cfg.DeepResearch.Limits, model)
		if e != nil {
			return nil, e
		}
		s.researchReads, err = inspection.NewResearchReader(readSource)
		if err != nil {
			return nil, err
		}
		s.researchReads.Authorize = s.progressReads.Authorize
		factory := func(b research.Binding) research.Retrieval {
			if deps.ResearchWeb != nil {
				return deps.ResearchWeb
			}
			return researchWebAdapter{web: webRuntime, actor: identity.ActorID(b.Actor)}
		}
		deepOption = workflow.WithDeepResearch(engine, factory, s.recordResearch, s.researchReadTool())
	}
	s.workflow = workflow.New(context.WithoutCancel(ctx), c,
		agent.Spec{Provider: implementor, Prompt: cfg.Implementor.Prompt, Tools: slices.Concat(local, messaging, deps.Implementor.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit)},
		agent.Spec{Provider: auditor, Prompt: cfg.Auditor.Prompt, Tools: slices.Concat(messaging, withoutWrites, deps.Auditor.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit)}, deepOption, workflow.WithEvidenceLookup(s.lookupExecutionEvidence), workflow.WithResearchDiagnostic(researchShell, cfg.ResearchExecution.MaxTimeout), workflow.WithProgressCurrent(func(actor identity.ActorID, id work.ID) (work.Work, error) {
			return s.GetWork(context.Background(), actor, id)
		}), workflow.WithProgressReporting(cfg.WorkProgressReporting), workflow.WithProgressTools([]tool.Tool{tool.GetWorkProgress(s.progressReadTool(false)), tool.GetResearchBrief(s.progressReadTool(true))}), workflow.WithAdmission(s.admission), workflow.WithPublisher(s.publish), workflow.WithResearcher(agent.Spec{Provider: researcher, Prompt: cfg.Researcher.Prompt, Tools: slices.Concat(researchReads, messaging, deps.Researcher.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit)}))
	rootTools := s.workflow.RootTools()
	for i, t := range rootTools {
		if t.Definition().Name == "wait_for_input" {
			rootTools[i] = tool.WaitForInputWhen(s.rootMayWait)
		}
	}
	rootSpec := agent.Spec{Provider: root, Prompt: cfg.Root.Prompt, ReasoningLimit: uint64(cfg.ReasoningLimit), Tools: slices.Concat(local, rootTools, []tool.Tool{tool.ListWork(func(ctx context.Context, c tool.Call, q work.ListQuery) (tool.Result, error) {
		v, e := s.ListWork(ctx, c.Actor, q)
		if e != nil {
			return tool.Result{}, e
		}
		return tool.JSON(v)
	})}, messaging, managementTools(s), deps.Root.Tools)}
	_, err = c.CreateAgent(message.User, rootSpec)
	if err != nil {
		return nil, err
	}
	if err = s.workflow.RegisterRoot(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	implSpec, auditSpec := s.workflow.Specs()
	s.effective = EffectiveConfig{DeepResearch: cfg.DeepResearch, LSP: describeLSP(cfg.LSP), ToolContractVersion: tool.InputContractVersion, ResearchExecution: cfg.ResearchExecution, WorkProgressReporting: cfg.WorkProgressReporting, Dir: cfg.Dir, ReasoningLimit: cfg.ReasoningLimit, Telemetry: cfg.Telemetry, Events: cfg.Events, Root: describeRole(cfg.roleModel(cfg.Root), rootSpec, injected(deps.Root)), Implementor: describeRole(cfg.roleModel(cfg.Implementor), implSpec, injected(deps.Implementor)), Auditor: describeRole(cfg.roleModel(cfg.Auditor), auditSpec, injected(deps.Auditor)), Researcher: describeRole(cfg.roleModel(cfg.Researcher), s.workflow.ResearcherSpec(), injected(deps.Researcher))}
	if cfg.DeepResearch.Enabled {
		deepModel := cfg.roleModel(cfg.Researcher)
		if cfg.DeepResearch.Model != nil {
			deepModel = *cfg.DeepResearch.Model
		}
		injectedDeep := deps.DeepResearchProvider != nil || cfg.DeepResearch.Model == nil && (deps.Researcher.Provider != nil || deps.Provider != nil) || cfg.DeepResearch.Model != nil && deps.Provider != nil
		s.effective.DeepResearch.Model = describeRole(deepModel, agent.Spec{}, injectedDeep).Model
	} else {
		// Unused configuration must not expose credentials either.
		s.effective.DeepResearch.Model = nil
	}
	if err = s.encoder.PublishConfiguration(context.Background(), s.Configuration()); err != nil {
		s.log.Fail(err)
		return nil, err
	}
	s.mu.Lock()
	s.stopOwner = context.AfterFunc(ctx, func() { s.startCloseReason("owner_cancelled") })
	s.mu.Unlock()
	return s, nil
}

func (s *Session) Config() Config         { return cloneConfig(s.config) }
func (s *Session) Root() identity.ActorID { return s.controller.Root() }
func (s *Session) Send(to identity.ActorID, text string) (message.Receipt, error) {
	s.interactionMu.Lock()
	defer s.interactionMu.Unlock()
	run, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return message.Receipt{}, interruptionError(err)
	}
	defer done()
	s.mu.Lock()
	if run.Err() != nil {
		err := ErrInterrupted
		if s.state != Open {
			err = ErrClosed
		}
		s.mu.Unlock()
		return message.Receipt{}, err
	}
	previous := s.interruption
	if previous != nil {
		s.interruption = nil
		s.workflow.ResumeInterrupted()
	}
	s.mu.Unlock()
	r, err := s.controller.SendContext(run, to, text)
	s.mu.Lock()
	if err != nil && previous != nil && s.interruption == nil {
		s.interruption = previous
		s.workflow.Suspend()
	}
	s.mu.Unlock()
	return r, err
}
func (s *Session) Agents() []AgentInfo {
	reader, v, err := s.traceView(context.Background(), eventlog.Cursor{})
	if err != nil {
		return nil
	}
	defer reader.Close(context.Background())
	out, err := v.Agents(context.Background())
	if err != nil {
		return nil
	}
	return out
}
func (s *Session) CreateAgent(ctx context.Context, actor identity.ActorID, request roster.CreateRequest) (roster.Registration, error) {
	return s.workflow.CreateAgent(ctx, actor, request)
}
func (s *Session) InspectAgent(id identity.ActorID, opts conversation.InspectOptions) (AgentInspection, error) {
	return s.InspectAgentContext(context.Background(), id, opts)
}
func (s *Session) PauseAgent(id identity.ActorID) (conversation.AgentInfo, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.AgentInfo{}, interruptionError(err)
	}
	defer done()
	return s.controller.PauseAgent(id)
}
func (s *Session) ResumeAgent(id identity.ActorID) (conversation.AgentInfo, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.AgentInfo{}, interruptionError(err)
	}
	defer done()
	return s.controller.ResumeAgent(id)
}
func (s *Session) StopAgent(id identity.ActorID) (conversation.AgentInfo, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.AgentInfo{}, interruptionError(err)
	}
	defer done()
	return s.controller.StopAgent(id)
}
func (s *Session) Receipt(id message.MessageID) (message.Receipt, bool) {
	_ = s.project(context.Background())
	return s.projection.Receipt(id)
}
func (s *Session) CountAgentTokens(ctx context.Context, id identity.ActorID, revision uint64) (int64, error) {
	run, done, err := s.admission.Begin(ctx)
	if err != nil {
		return 0, interruptionError(err)
	}
	defer done()
	return s.controller.CountAgentTokens(run, id, revision)
}

// rootMayWait rejects wait_for_input while the root owns no live work. Nothing
// would arrive, and in ladder runs a root that ended a finished task this way
// sat idle until the session budget expired. Listing problems never block a
// wait; only a definite absence of live work does.
func (s *Session) rootMayWait(ctx context.Context, c tool.Call) error {
	page, err := s.ListWork(ctx, c.Actor, work.ListQuery{Limit: 100})
	if err != nil {
		return nil
	}
	for _, item := range page.Items {
		if !item.State.Terminal() {
			return nil
		}
	}
	return errors.New("wait_for_input rejected: you own no active delegated work, so no worker result can arrive. If the task is finished, send the final reply as a text-only response now; if work remains, assign it first")
}

func localToolsWithChanges(dir string, changed func(string), afterRun func()) ([]tool.Tool, error) {
	shell, err := tool.NewShell(tool.ShellConfig{Dir: dir, AfterRun: afterRun})
	if err != nil {
		return nil, err
	}
	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir, OnChange: changed})
	if err != nil {
		return nil, err
	}
	pdf, err := tool.NewPDF(tool.PDFConfig{Dir: dir})
	if err != nil {
		return nil, err
	}
	return append([]tool.Tool{shell, pdf}, files.Tools()...), nil
}

func cloneConfig(c Config) Config {
	if c.LSP != nil {
		v := c.LSP.Clone()
		c.LSP = &v
	}
	if c.DeepResearch.Model != nil {
		m := cloneModel(*c.DeepResearch.Model)
		c.DeepResearch.Model = &m
	}
	c.ResearchExecution.Env = slices.Clone(c.ResearchExecution.Env)
	c.Model = cloneModel(c.Model)
	if c.Web != nil {
		v := *c.Web
		c.Web = &v
	}
	for _, r := range []*AgentConfig{&c.Root, &c.Implementor, &c.Auditor, &c.Researcher} {
		r.Prompt = r.Prompt.Clone()
		if r.Model != nil {
			v := cloneModel(*r.Model)
			r.Model = &v
		}
	}
	return c
}
func cloneModel(m ModelConfig) ModelConfig {
	m.Generation = m.Generation.Clone()
	return m
}

// Clone returns an independent configuration for host assembly/adapters.
func (c Config) Clone() Config { return cloneConfig(c) }
func (c Config) roleModel(r AgentConfig) ModelConfig {
	if r.Model != nil {
		return *r.Model
	}
	return c.Model
}
