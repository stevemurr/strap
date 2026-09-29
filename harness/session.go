// Package harness assembles one independently running manager and its audited work.
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

type Config struct {
	DeepResearch          DeepResearchConfig          `json:"deep_research"`
	LSP                   *lsp.Config                 `json:"lsp,omitempty"` // Nil disables experimental language tools.
	WorkProgressReporting WorkProgressReportingConfig `json:"work_progress_reporting"`
	Telemetry             TelemetryConfig             `json:"telemetry"`
	Events                EventConfig                 `json:"events"`
	Dir                   string                      `json:"dir"`
	ReasoningLimit        int                         `json:"reasoning_limit"` // Reasoning bytes per model call; zero is unlimited.
	Model                 ModelConfig                 `json:"model"`
	LocalTools            bool                        `json:"local_tools"`
	FileEdits             tool.EditMode               `json:"file_edits,omitempty"` // Empty is tool.EditText.
	Web                   *tool.WebConfig             `json:"web"`                  // Nil disables browser/search tools.
	// Solo runs one agent with every tool, which the user talks to and which
	// does the work itself, in place of the manager and its workers.
	Solo  bool        `json:"solo,omitempty"`
	Agent AgentConfig `json:"agent"` // Used only in a solo session.
	// Tester runs an adversarial tester, with the agent's model, before a solo
	// agent's reply to a code change; the failures it reports that the
	// harness reproduces go back to the agent. Experimental.
	Tester bool `json:"tester,omitempty"`
	// TesterBudget bounds one tester run's wall clock; zero is DefaultTesterBudget.
	TesterBudget time.Duration `json:"tester_budget_ns,omitempty"`
	// TesterReportAll has the tester report every failing test and leave
	// judging it against the requirements to the agent. Experimental.
	TesterReportAll bool        `json:"tester_report_all,omitempty"`
	Manager         AgentConfig `json:"manager"`
	Debugger        AgentConfig `json:"debugger"`
	DebugToolkit    bool        `json:"debug_toolkit,omitempty"` // Start a debugger beside the manager.
	// ManualAudits gives the manager assign_audit and auditors to create,
	// instead of the harness assigning every submission's audit itself. The
	// interaction suite's audit scenarios measure that older protocol.
	ManualAudits bool `json:"manual_audits,omitempty"`
	// AuditBrief hands each auditor the requirements, the submission's
	// summary and the changed files' contents with its assignment.
	AuditBrief bool `json:"audit_brief,omitempty"`
	// AuditRuns lists the implementor's final build, vet and test runs, as the
	// harness recorded them, in each audit's context, so the auditor does not
	// run them again.
	AuditRuns      bool        `json:"audit_runs,omitempty"`
	Implementor    AgentConfig `json:"implementor"`
	Auditor        AgentConfig `json:"auditor"`
	WebResearcher  AgentConfig `json:"web_researcher"`
	DeepResearcher AgentConfig `json:"deep_researcher"` // Used only when deep research is enabled.
	Experimenter   AgentConfig `json:"experimenter"`    // Used only with local tools.
	Reviewer       AgentConfig `json:"reviewer"`        // Used only with local tools.
	// Seed makes the engine deterministic: every work-store id and derived
	// secret is a function of it. Empty draws a random seed. The trace records
	// it in session_started, so a replay reuses the recorded one.
	Seed []byte `json:"seed,omitempty"`
}

// DefaultConfig returns independent library defaults without acquiring resources.
// The CLI selects its model from its own model catalog.
func DefaultConfig() Config {
	languages := lsp.DefaultConfig()
	return Config{DeepResearch: DeepResearchConfig{Enabled: true}, WorkProgressReporting: workflow.DefaultWorkProgressReporting(), Telemetry: TelemetryConfig{ContextTokens: true, Concurrency: 2, Queue: 128, Timeout: 10 * time.Second, ServerMetricsInterval: 5 * time.Second}, Events: EventConfig{Queue: eventlog.Limits{Entries: 1024, Bytes: 8 << 20}}, Dir: ".", ReasoningLimit: 192 << 10, Model: ModelConfig{Backend: "vllm", BaseURL: "http://127.0.0.1:8000", Model: "qwen3.6", Timeout: 60 * time.Minute}, LocalTools: true, AuditRuns: true, Web: &tool.WebConfig{}, LSP: &languages,
		Agent: AgentConfig{Prompt: agentPrompt.Clone()}, Manager: AgentConfig{Prompt: managerPrompt.Clone()}, Debugger: AgentConfig{Prompt: debuggerPrompt.Clone()}, Implementor: AgentConfig{Prompt: executionPrompt.Clone()}, Auditor: AgentConfig{Prompt: auditorPrompt.Clone()}, WebResearcher: AgentConfig{Prompt: webResearcherPrompt.Clone()}, DeepResearcher: AgentConfig{Prompt: deepResearcherPrompt.Clone()}, Experimenter: AgentConfig{Prompt: experimenterPrompt.Clone()}, Reviewer: AgentConfig{Prompt: reviewerPrompt.Clone()}}
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

// Clock is the time the harness schedules progress notices by; see
// Dependencies.Clock.
type Clock interface {
	Now() time.Time
	Timer(at time.Time) (fired <-chan time.Time, stop func())
}

// Dependencies are executable collaborators. Providers/tools are borrowed unless
// also registered in Resources. Resource ownership transfers when New is called,
// including when construction fails and returns a cleanup handle.
type Dependencies struct {
	DeepResearchProvider                                   provider.Provider
	ResearchWeb                                            research.Retrieval
	LSP                                                    lsp.Dependencies
	CaptureFailure                                         func(error)       // Optional independent diagnostic sink; must return promptly.
	Provider                                               provider.Provider // Shared fallback for all roles, useful for eval fakes.
	Agent, Tester, Manager, Debugger, Implementor, Auditor AgentDependencies
	WebResearcher, DeepResearcher, Experimenter, Reviewer  AgentDependencies
	EventStore                                             func(sessionID string) (eventlog.Store, error) // Factory transfers storage ownership; called once.
	Resources                                              []OwnedResource
	// TesterRuns serves adversarial tester runs in place of running them: a
	// replay answers each with the run its recording made, which is all the
	// agent ever saw of it. It reports false when none is left to serve.
	TesterRuns func(identity.ActorID) (conversation.TesterEvent, bool)
	// Environment wraps every tool that reaches outside the session: files,
	// shell, language servers and the web. Coordination and ledger tools are
	// the harness itself and are never wrapped. Replays use it to serve
	// recorded results.
	Environment func(tool.Tool) tool.Tool
	// Workspace lists the workspace an agent sees on its first wake, the one
	// environment read outside a tool. Nil lists the directory.
	Workspace func(actor identity.ActorID, dir string) *message.Workspace
	// Intake decides which queued messages each agent takes when it reads its
	// inbox; see agent.Intake. Replays use it to settle message races the way
	// the recording settled them.
	Intake func(identity.ActorID) agent.Intake
	// SendOrder orders message admission; see conversation.SendOrder.
	SendOrder conversation.SendOrder
	// Sequence orders each agent's tool starts and message consumption; see
	// agent.Sequence.
	Sequence func(identity.ActorID) agent.Sequence
	// Clock schedules progress notices; nil is the wall clock. A replay runs
	// a virtual clock so notices fall where they fell when recorded.
	Clock Clock
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
	listWorkspace    func(identity.ActorID, string) *message.Workspace
	finish           *finishFacts // A solo session's finish check; nil otherwise.
	telemetry        *telemetry
	changes          changeLog
	briefFiles       tool.Tool
	serverMetrics    *serverMetrics
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
	manager          identity.ActorID
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
	// The manager and the web and deep researchers read no files; a question
	// about the workspace goes to a reviewer.
	for _, role := range []*AgentConfig{&cfg.Implementor, &cfg.Auditor, &cfg.Experimenter, &cfg.Reviewer} {
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
	if err = cfg.WorkProgressReporting.Validate(); err != nil {
		return nil, err
	}
	if cfg.ReasoningLimit < 0 {
		return nil, errors.New("reasoning limit must not be negative")
	}
	// Web-disabled hosts stay offline unless they explicitly inject retrieval.
	if cfg.Web == nil && deps.ResearchWeb == nil {
		cfg.DeepResearch.Enabled = false
	}
	if cfg.DeepResearch.Enabled {
		cfg.DeepResearch.Limits, err = cfg.DeepResearch.Limits.Resolve()
		if err != nil {
			return nil, err
		}
		if !slices.Contains(cfg.Manager.Prompt.Instructions, deepResearchManagerInstruction) {
			cfg.Manager.Prompt.Instructions = append(cfg.Manager.Prompt.Instructions, deepResearchManagerInstruction)
		}
	}
	if cfg.AuditBrief && !slices.Contains(cfg.Auditor.Prompt.Instructions, auditBriefInstruction) {
		cfg.Auditor.Prompt.Instructions = append(cfg.Auditor.Prompt.Instructions, auditBriefInstruction)
	}
	if cfg.AuditRuns && !slices.Contains(cfg.Auditor.Prompt.Instructions, auditRunsInstruction) {
		cfg.Auditor.Prompt.Instructions = append(cfg.Auditor.Prompt.Instructions, auditRunsInstruction)
	}
	if cfg.Solo && cfg.Web != nil && !slices.Contains(cfg.Agent.Prompt.Instructions, agentWebInstruction) {
		cfg.Agent.Prompt.Instructions = append(cfg.Agent.Prompt.Instructions, agentWebInstruction)
	}
	s.config.DeepResearch = cfg.DeepResearch
	s.config.Agent = cfg.Agent
	s.config.Manager = cfg.Manager
	s.config.Auditor = cfg.Auditor
	s.config.Debugger = cfg.Debugger
	s.config.WebResearcher = cfg.WebResearcher
	s.config.DeepResearcher = cfg.DeepResearcher
	s.config.Experimenter = cfg.Experimenter
	s.config.Reviewer = cfg.Reviewer
	if len(s.config.Seed) == 0 {
		s.config.Seed = make([]byte, 32)
		if _, err = rand.Read(s.config.Seed); err != nil {
			return nil, err
		}
	}
	// The session id derives from the seed like every other id, so a replay
	// of a recording reads under the same id the recording did.
	key := work.DeriveKey(s.config.Seed, "session")
	s.id = hex.EncodeToString(key[:16])
	s.listWorkspace = deps.Workspace
	if s.listWorkspace == nil {
		s.listWorkspace = func(_ identity.ActorID, dir string) *message.Workspace { return workspaceListing(dir) }
	}
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
		ID   string `json:"id"`
		Seed []byte `json:"seed"`
	}{s.id, s.config.Seed})
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
		p, err := model.NewProvider(&http.Client{Transport: pool, Timeout: model.Timeout})
		if err != nil {
			return nil, err
		}
		// A retried call is made again unchanged; injected providers, such as
		// a replay's recorded outputs, answer as recorded.
		return provider.WithRetries(p, modelRetries), nil
	}
	manager, err := makeProvider(cfg.Manager, deps.Manager)
	if err != nil {
		return nil, err
	}
	var solo provider.Provider
	if cfg.Solo {
		if solo, err = makeProvider(cfg.Agent, deps.Agent); err != nil {
			return nil, err
		}
	}
	var debugger provider.Provider
	if cfg.DebugToolkit {
		if debugger, err = makeProvider(cfg.Debugger, deps.Debugger); err != nil {
			return nil, err
		}
	}
	implementor, err := makeProvider(cfg.Implementor, deps.Implementor)
	if err != nil {
		return nil, err
	}
	auditor, err := makeProvider(cfg.Auditor, deps.Auditor)
	if err != nil {
		return nil, err
	}
	webResearcher, err := makeProvider(cfg.WebResearcher, deps.WebResearcher)
	if err != nil {
		return nil, err
	}
	deepResearcher, err := makeProvider(cfg.DeepResearcher, deps.DeepResearcher)
	if err != nil {
		return nil, err
	}
	experimenter, err := makeProvider(cfg.Experimenter, deps.Experimenter)
	if err != nil {
		return nil, err
	}
	reviewer, err := makeProvider(cfg.Reviewer, deps.Reviewer)
	if err != nil {
		return nil, err
	}
	var languages *lsp.Manager
	var languageTools []tool.Tool
	var changed func(string)
	var afterRun func()
	// startLanguages starts a language manager rooted at dir: a workspace
	// copy's, or a scratch project's outside the workspace.
	var startLanguages func(dir string) (*lsp.Manager, error)
	if cfg.LSP != nil {
		startLanguages = func(dir string) (*lsp.Manager, error) {
			c := cfg.LSP.Clone()
			c.Dir = dir
			return lsp.New(c, deps.LSP)
		}
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
	var local, webTools []tool.Tool
	if cfg.LocalTools {
		local, err = localToolsWithChanges(cfg.Dir, cfg.FileEdits, changed, afterRun)
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
		// The web belongs to the web researcher alone: what the session learns
		// from it arrives as research with recorded claims and sources. The
		// deep researcher reaches it only through the deep research engine.
		webTools = web.Tools()
	}
	var scratch *scratchLanguages
	if languages != nil {
		// Writes come back with the errors the language server then reports.
		scratch = newScratchLanguages(startLanguages)
		s.resources.Add("scratch lsp", scratch)
		checkWrites(local, languages, scratch, cfg.Dir)
		checkShell(local, languages, scratch, cfg.Dir)
		for i, t := range local {
			switch t.Definition().Name {
			case "write_file", "edit_file":
				local[i] = describedTool{Tool: t, note: writeCheckNote}
			case "shell":
				local[i] = describedTool{Tool: t, note: shellCheckNote}
			}
		}
	}
	for _, tools := range [][]tool.Tool{local, webTools} {
		for i, t := range tools {
			if deps.Environment != nil {
				t = deps.Environment(t)
			}
			tools[i] = s.recordEnvironment(t)
		}
	}
	c := conversation.New(execution, conversation.WithRoute(s.route), conversation.WithInboxAdmission(s.inboxAdmission), conversation.WithWakeContext(s.wakeContext), conversation.WithIntake(deps.Intake), conversation.WithSendOrder(deps.SendOrder), conversation.WithSequence(deps.Sequence), conversation.WithReporting(conversation.ReporterFunc(func(_ context.Context, e conversation.Event) error { return s.publish(e) }), s.readWorkflow))
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
	s.progressReads.UseKey(work.DeriveKey(cfg.Seed, "progress-cursor"))
	s.progressReads.Evidence = inspection.ReadExecutionEvidence
	s.progressReads.Authorize = func(ctx context.Context, actor identity.ActorID, id work.ID) error {
		_, err := s.GetWork(ctx, actor, id)
		return err
	}
	s.progressReads.AuthorizeEvidence = func(ctx context.Context, actor identity.ActorID, id work.ID) error {
		view, err := s.workView(ctx)
		if err != nil {
			return err
		}
		return view.CanReadExecution(actor, id)
	}
	messaging := []tool.Tool{tool.SendMessage(s.resolveRole), tool.MessageStatus(c.Receipt)}
	// Implementors alone change the workspace; auditors and experimenters
	// change their own copies of it. Reviewers read it without a shell, which
	// writes as easily as write_file. The manager reads nothing: a question
	// about the workspace goes to a reviewer.
	reads := slices.DeleteFunc(slices.Clone(local), func(t tool.Tool) bool {
		n := t.Definition().Name
		return n == "write_file" || n == "edit_file" || n == "shell"
	})
	// Auditors and experimenters work in their own copy of the workspace, with
	// language servers of its own; see isolation.
	var auditorLocal, experimenterLocal []tool.Tool
	var methodFiles workflow.MethodFiles
	if cfg.LocalTools {
		iso, err := newIsolation(cfg.Dir, cfg.FileEdits, shellMaxTimeout, func(actor identity.ActorID) (work.Work, bool, error) {
			return s.workflow.Store.AdmitExecution(actor)
		}, func(id work.ID, at work.Revision) bool { return s.workflow.Store.Holds(id, at) }, startLanguages)
		if err != nil {
			return nil, err
		}
		s.resources.Add("workspace copies", iso)
		templates, err := localToolsWithChanges(cfg.Dir, cfg.FileEdits, nil, nil)
		if err != nil {
			return nil, err
		}
		for _, t := range templates {
			if t.Definition().Name == "shell" {
				templates = append(templates, trialsTool(t, iso.maxTimeout))
				break
			}
		}
		// The session's language tools supply the definitions; calls go to the
		// caller's copy and its own servers.
		templates = append(templates, languageTools...)
		// Experimenters write only in their copy; auditors may also write
		// outside it, such as scratch programs in /tmp.
		isolated := func(outside bool, note string) []tool.Tool {
			tools := iso.tools(templates, outside)
			for i, t := range tools {
				n := t.Definition().Name
				if n == "shell" || n == "write_file" || n == "run_trials" {
					t = describedTool{Tool: t, note: note}
				}
				if languages != nil && (n == "write_file" || n == "edit_file") {
					t = describedTool{Tool: t, note: writeCheckNote}
				}
				if languages != nil && n == "shell" {
					t = describedTool{Tool: t, note: shellCheckNote}
				}
				if deps.Environment != nil {
					t = deps.Environment(t)
				}
				tools[i] = s.recordEnvironment(t)
			}
			return tools
		}
		experimenterLocal = isolated(false, isolatedDescription)
		auditorLocal = slices.DeleteFunc(isolated(true, auditorIsolatedDescription), func(t tool.Tool) bool { return t.Definition().Name == "run_trials" })
		methodFiles = iso.methodFiles
	}
	var deepOption workflow.Option = func(*workflow.Session) {}
	if cfg.DeepResearch.Enabled {
		model := deepResearcher
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
		engine.UseIDs(work.SeededIDs(append(slices.Clone(cfg.Seed), "research"...)))
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
		agent.Spec{Provider: implementor, Prompt: cfg.Implementor.Prompt, Tools: slices.Concat(local, messaging, deps.Implementor.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit), Concurrent: readTools(local)},
		agent.Spec{Provider: auditor, Prompt: cfg.Auditor.Prompt, Tools: slices.Concat(messaging, auditorLocal, deps.Auditor.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit), Concurrent: readTools(auditorLocal)}, deepOption, workflow.WithEvidenceLookup(s.lookupExecutionEvidence), autoAudit(cfg), auditBriefs(s, cfg, deps), workflow.WithProgressCurrent(func(actor identity.ActorID, id work.ID) (work.Work, error) {
			return s.GetWork(context.Background(), actor, id)
		}), workflow.WithProgressReporting(cfg.WorkProgressReporting), workflow.WithProgressTools([]tool.Tool{tool.GetWorkProgress(s.progressReadTool(false)), tool.GetBrief(s.progressReadTool(true)), tool.GetConclusion(s.conclusionReadTool)}), workflow.WithAdmission(s.admission), workflow.WithPublisher(s.publish), workflow.WithIDs(work.SeededIDs(cfg.Seed)), workflow.WithClock(deps.Clock), workflow.WithWebResearcher(agent.Spec{Provider: webResearcher, Prompt: cfg.WebResearcher.Prompt, Tools: slices.Concat(webTools, messaging, deps.WebResearcher.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit), Concurrent: []string{"web_search", "open_url"}}),
		workflow.WithDeepResearcher(agent.Spec{Provider: deepResearcher, Prompt: cfg.DeepResearcher.Prompt, Tools: slices.Concat(messaging, deps.DeepResearcher.Tools), ReasoningLimit: uint64(cfg.ReasoningLimit)}),
		workflow.WithExperimenter(experimenterSpec(cfg, experimenter, slices.Concat(experimenterLocal, messaging, deps.Experimenter.Tools)), methodFiles),
		workflow.WithReviewer(reviewerSpec(cfg, reviewer, slices.Concat(reads, messaging, deps.Reviewer.Tools))))
	// The harness assigns audits itself (workflow.WithAutoAudit), so the
	// manager holds no tool to assign one.
	coordination := slices.DeleteFunc(s.workflow.CoordinationTools(), func(t tool.Tool) bool { return !cfg.ManualAudits && t.Definition().Name == "assign_audit" })
	for i, t := range coordination {
		if t.Definition().Name == "wait_for_input" {
			coordination[i] = tool.WaitForInputWhen(s.managerMayWait)
		}
	}
	listWork := tool.ListWork(func(ctx context.Context, c tool.Call, q work.ListQuery) (tool.Result, error) {
		v, e := s.ListWork(ctx, c.Actor, q)
		if e != nil {
			return tool.Result{}, e
		}
		return modelJSON(v)
	})
	s.workflow.UseManager(agent.Spec{Provider: manager, Prompt: cfg.Manager.Prompt, ReasoningLimit: uint64(cfg.ReasoningLimit), ReplyCheck: s.managerReplyCheck, Tools: slices.Concat(coordination, []tool.Tool{listWork}, messaging, agentControls(s, s.graph), agentReads(s, s.graph), deps.Manager.Tools)})
	// A solo session's one agent holds every tool itself; the finish check
	// reads the facts its writes and commands leave behind.
	var soloSpec agent.Spec
	if cfg.Solo {
		s.finish = newFinishFacts(cfg.Dir, languages, scratch)
		// The tester runs for a request that asks for it, or for every
		// request with cfg.Tester; the finish check bounds its own holds.
		testerModel, err := makeProvider(cfg.Agent, deps.Tester)
		if err != nil {
			return nil, err
		}
		s.finish.tester = &tester{provider: testerModel, dir: s.finish.dir, edits: cfg.FileEdits, limit: uint64(cfg.ReasoningLimit), budget: cfg.TesterBudgetOrDefault(), reportAll: cfg.TesterReportAll, publish: s.publish, recorded: deps.TesterRuns}
		s.finish.always = cfg.Tester
		// An unchecked change, the tester's failures, then the fix unchecked.
		checks := 3
		var tools []tool.Tool
		for _, t := range slices.Concat(local, webTools, deps.Agent.Tools) {
			tools = append(tools, factTool{Tool: t, facts: s.finish})
		}
		tools = append(tools, tool.UpdateTodos(s.updateTodos))
		soloSpec = agent.Spec{Provider: solo, Prompt: cfg.Agent.Prompt, ReasoningLimit: uint64(cfg.ReasoningLimit), ReplyCheck: s.finish.check, ReplyChecks: checks, Tools: tools, Concurrent: append(readTools(local), "web_search", "open_url")}
	}
	var debuggerSpec agent.Spec
	if cfg.DebugToolkit {
		debuggerSpec = agent.Spec{Provider: debugger, Prompt: cfg.Debugger.Prompt, ReasoningLimit: uint64(cfg.ReasoningLimit), Tools: slices.Concat(messaging, s.supervisorReads(), agentReads(s, s.graph), agentControls(s, s.graph), deps.Debugger.Tools)}
	}
	if err = s.bootstrap(ctx, soloSpec, debuggerSpec); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	implSpec, auditSpec := s.workflow.Specs()
	s.effective = EffectiveConfig{DeepResearch: cfg.DeepResearch, LSP: describeLSP(cfg.LSP), ToolContractVersion: tool.InputContractVersion, WorkProgressReporting: cfg.WorkProgressReporting, Dir: cfg.Dir, LocalTools: cfg.LocalTools, FileEdits: cfg.FileEdits, Web: cfg.Web, DebugToolkit: cfg.DebugToolkit, ReasoningLimit: cfg.ReasoningLimit, Telemetry: cfg.Telemetry, Events: cfg.Events, Manager: describeRole(cfg.roleModel(cfg.Manager), s.workflow.ManagerSpec(), injected(deps.Manager)), Implementor: describeRole(cfg.roleModel(cfg.Implementor), implSpec, injected(deps.Implementor)), Auditor: describeRole(cfg.roleModel(cfg.Auditor), auditSpec, injected(deps.Auditor)), WebResearcher: describeRole(cfg.roleModel(cfg.WebResearcher), s.workflow.WebResearcherSpec(), injected(deps.WebResearcher))}
	if cfg.LocalTools {
		e := describeRole(cfg.roleModel(cfg.Experimenter), s.workflow.ExperimenterSpec(), injected(deps.Experimenter))
		s.effective.Experimenter = &e
		r := describeRole(cfg.roleModel(cfg.Reviewer), s.workflow.ReviewerSpec(), injected(deps.Reviewer))
		s.effective.Reviewer = &r
	}
	if cfg.Solo {
		a := describeRole(cfg.roleModel(cfg.Agent), soloSpec, injected(deps.Agent))
		s.effective.Agent, s.effective.Tester = &a, cfg.Tester
	}
	if cfg.DebugToolkit {
		d := describeRole(cfg.roleModel(cfg.Debugger), debuggerSpec, injected(deps.Debugger))
		s.effective.Debugger = &d
	}
	if cfg.DeepResearch.Enabled {
		d := describeRole(cfg.roleModel(cfg.DeepResearcher), s.workflow.DeepResearcherSpec(), injected(deps.DeepResearcher))
		s.effective.DeepResearcher = &d
		deepModel := cfg.roleModel(cfg.DeepResearcher)
		if cfg.DeepResearch.Model != nil {
			deepModel = *cfg.DeepResearch.Model
		}
		injectedDeep := deps.DeepResearchProvider != nil || cfg.DeepResearch.Model == nil && (deps.DeepResearcher.Provider != nil || deps.Provider != nil) || cfg.DeepResearch.Model != nil && deps.Provider != nil
		s.effective.DeepResearch.Model = describeRole(deepModel, agent.Spec{}, injectedDeep).Model
	} else {
		// Unused configuration must not expose credentials either.
		s.effective.DeepResearch.Model = nil
	}
	if err = s.encoder.PublishConfiguration(context.Background(), s.Configuration()); err != nil {
		s.log.Fail(err)
		return nil, err
	}
	s.serverMetrics = startServerMetrics(s, cfg, deps)
	s.mu.Lock()
	s.stopOwner = context.AfterFunc(ctx, func() { s.startCloseReason("owner_cancelled") })
	s.mu.Unlock()
	return s, nil
}

func (s *Session) Config() Config { return cloneConfig(s.config) }

// bootstrap brings up a session's permanent agents, like a boot loader: the
// manager, which the user talks to and which does everything that touches the
// project, and in a debug session a debugger beside it. It is a deterministic
// function, not an agent; it runs once, before any message, and is the only
// code that creates either. Registering each one adds it to the topology graph
// with the edges its role derives, and no agent can create, stop, pause or
// resume them.
func (s *Session) bootstrap(ctx context.Context, solo, debugger agent.Spec) error {
	create := s.workflow.CreateManager
	if solo.Provider != nil {
		create = func(ctx context.Context) (identity.ActorID, error) { return s.workflow.CreateSolo(ctx, solo) }
	}
	manager, err := create(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.manager = manager
	s.mu.Unlock()
	if debugger.Provider != nil {
		if _, err := s.workflow.CreateDebugger(ctx, debugger); err != nil {
			return err
		}
	}
	return nil
}

// graph is the session's agent topology, owned by the workflow's registry.
func (s *Session) graph() *roster.Graph {
	if s.workflow == nil {
		return nil
	}
	return s.workflow.Graph()
}

// Graph exposes the topology for inspection and tests.
func (s *Session) Graph() *roster.Graph { return s.graph() }

// route is the controller's delivery policy: a message needs the edge its kind
// needs, and the user hears only replies. An agent once asked the user a
// question with send_message in the middle of its turn, a second channel
// alongside its reply.
func (s *Session) route(from, to identity.ActorID, kind message.MessageKind) error {
	g := s.graph()
	if g == nil {
		return nil
	}
	if to == message.User && kind != message.Reply {
		return fmt.Errorf("%s cannot send the user a message; answer the user with a text reply", g.Describe(from))
	}
	// A final reply or failure answers whoever the agent works for; anything
	// it starts is a message. The manager holds only a reply edge to the user,
	// so it answers by replying and cannot also message the answer.
	return g.Check(from, to, roster.EdgeFor(kind))
}

// resolveRole turns the role names send_message accepts into agent ids.
func (s *Session) resolveRole(to identity.ActorID) identity.ActorID {
	g := s.graph()
	if g == nil {
		return to
	}
	switch to {
	case "manager":
		return g.Find(roster.Manager)
	case "debugger":
		return g.Find(roster.Debugger)
	}
	return to
}

// Debugger returns the session's debugger, which the user talks to beside the
// manager in a debug session, or "".
func (s *Session) Debugger() identity.ActorID {
	if g := s.graph(); g != nil {
		return g.Find(roster.Debugger)
	}
	return ""
}

// Manager returns the session's manager, the agent the user talks to.
func (s *Session) Manager() identity.ActorID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manager
}
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

// managerMayWait rejects wait_for_input while the manager owns no live work.
// Nothing would arrive, and in ladder runs a coordinator that ended a finished
// task this way sat idle until the session budget expired. Listing problems
// never block a wait; only a definite absence of live work does.
func (s *Session) managerMayWait(ctx context.Context, c tool.Call) error {
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

func localToolsWithChanges(dir string, edits tool.EditMode, changed func(string), afterRun func()) ([]tool.Tool, error) {
	shell, err := tool.NewShell(tool.ShellConfig{Dir: dir, AfterRun: afterRun})
	if err != nil {
		return nil, err
	}
	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir, OnChange: changed, Edits: edits})
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
	c.Model = cloneModel(c.Model)
	c.Seed = slices.Clone(c.Seed)
	if c.Web != nil {
		v := *c.Web
		c.Web = &v
	}
	for _, r := range []*AgentConfig{&c.Agent, &c.Manager, &c.Debugger, &c.Implementor, &c.Auditor, &c.WebResearcher, &c.DeepResearcher, &c.Experimenter, &c.Reviewer} {
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
