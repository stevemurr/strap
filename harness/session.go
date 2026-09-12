// Package harness assembles one independently running root and its audited work.
// Terminal and transport adapters do not select execution policy.
package harness

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/admission"
	"github.com/stevemurr/strap/internal/resource"
	"github.com/stevemurr/strap/internal/transport"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type Resource = resource.Resource

type AgentConfig struct {
	Prompt prompt.Prompt
	Model  *ModelConfig // Nil uses the session model configuration.
}

type Config struct {
	Dir                        string
	Model                      ModelConfig
	LocalTools                 bool
	Web                        *tool.WebConfig // Nil disables browser/search tools.
	Root, Implementor, Auditor AgentConfig
}

// DefaultConfig returns independent CLI-compatible defaults without acquiring resources.
func DefaultConfig() Config {
	return Config{Dir: ".", Model: ModelConfig{Backend: "vllm", BaseURL: "http://192.168.1.237:8355", Model: "qwen3.6", Timeout: 60 * time.Minute}, LocalTools: true, Web: &tool.WebConfig{},
		Root: AgentConfig{Prompt: rootPrompt.Clone()}, Implementor: AgentConfig{Prompt: executionPrompt.Clone()}, Auditor: AgentConfig{Prompt: auditorPrompt.Clone()}}
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
	Provider                   provider.Provider // Shared fallback for all roles, useful for eval fakes.
	Root, Implementor, Auditor AgentDependencies
	Resources                  []OwnedResource
}

type Session struct {
	config          Config
	controller      *conversation.Controller
	workflow        *workflow.Session
	resources       *resource.Group
	mu              sync.Mutex
	state           State
	attempt         *closeAttempt
	admission       *admission.Gate
	cancelExecution context.CancelFunc
	stopOwner       func() bool
}

// StartupError retains cleanup ownership if rollback could not complete.
// Call Close again through errors.As; a partially built session is never usable.
type StartupError struct {
	cause   error
	cleanup Resource
}

func (e *StartupError) Error() string                   { return e.cause.Error() }
func (e *StartupError) Unwrap() error                   { return e.cause }
func (e *StartupError) Close(ctx context.Context) error { return e.cleanup.Close(ctx) }

func New(ctx context.Context, cfg Config, deps Dependencies) (_ *Session, err error) {
	execution, cancelExecution := context.WithCancel(context.WithoutCancel(ctx))
	s := &Session{config: cloneConfig(cfg), resources: resource.New(), state: Open, cancelExecution: cancelExecution, admission: admission.New(execution)}
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cleanupErr := s.Close(cleanup); cleanupErr != nil {
			err = &StartupError{cause: errors.Join(err, cleanupErr), cleanup: s}
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
	pool := transport.New()
	s.resources.Add("provider transport", pool)
	cfg = s.config
	makeProvider := func(role AgentConfig, injected provider.Provider) (provider.Provider, error) {
		if injected != nil {
			return injected, nil
		}
		if deps.Provider != nil {
			return deps.Provider, nil
		}
		model := cfg.Model
		if role.Model != nil {
			model = *role.Model
		}
		if model.Timeout <= 0 {
			return nil, errors.New("model timeout must be positive")
		}
		return model.NewProvider(&http.Client{Transport: pool, Timeout: model.Timeout})
	}
	root, err := makeProvider(cfg.Root, deps.Root.Provider)
	if err != nil {
		return nil, err
	}
	implementor, err := makeProvider(cfg.Implementor, deps.Implementor.Provider)
	if err != nil {
		return nil, err
	}
	auditor, err := makeProvider(cfg.Auditor, deps.Auditor.Provider)
	if err != nil {
		return nil, err
	}
	var local []tool.Tool
	if cfg.LocalTools {
		local, err = localTools(cfg.Dir)
		if err != nil {
			return nil, err
		}
	}
	if cfg.Web != nil {
		web, e := tool.NewWeb(*cfg.Web)
		if e != nil {
			return nil, e
		}
		s.resources.Add("web", web)
		local = append(local, web.Tools()...)
	}
	c := conversation.New(execution)
	s.controller = c
	messaging := []tool.Tool{tool.SendMessage(), tool.MessageStatus(c.Receipt)}
	withoutWrites := slices.DeleteFunc(slices.Clone(local), func(t tool.Tool) bool { n := t.Definition().Name; return n == "write_file" || n == "edit_file" })
	s.workflow = workflow.New(context.WithoutCancel(ctx), c,
		agent.Spec{Provider: implementor, Prompt: cfg.Implementor.Prompt, Tools: slices.Concat(local, messaging, deps.Implementor.Tools)},
		agent.Spec{Provider: auditor, Prompt: cfg.Auditor.Prompt, Tools: slices.Concat(messaging, withoutWrites, deps.Auditor.Tools)}, workflow.WithAdmission(s.admission))
	_, err = c.CreateAgent(message.User, agent.Spec{Provider: root, Prompt: cfg.Root.Prompt, Tools: slices.Concat(local, s.workflow.RootTools(), messaging, managementTools(s), deps.Root.Tools)})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.stopOwner = context.AfterFunc(ctx, func() { s.startClose() })
	s.mu.Unlock()
	return s, nil
}

func (s *Session) Config() Config         { return cloneConfig(s.config) }
func (s *Session) Root() identity.ActorID { return s.controller.Root() }
func (s *Session) Send(to identity.ActorID, text string) (message.Receipt, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return message.Receipt{}, err
	}
	defer done()
	return s.controller.Send(to, text)
}
func (s *Session) Agents() []conversation.AgentInfo { return s.controller.Agents() }
func (s *Session) CreateAgent(parent identity.ActorID, spec agent.Spec) (conversation.Creation, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.Creation{}, err
	}
	defer done()
	return s.controller.CreateAgent(parent, spec)
}
func (s *Session) InspectAgent(id identity.ActorID, opts conversation.InspectOptions) (conversation.AgentInspection, error) {
	return s.controller.InspectAgent(id, opts)
}
func (s *Session) PauseAgent(id identity.ActorID) (conversation.AgentInfo, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.AgentInfo{}, err
	}
	defer done()
	return s.controller.PauseAgent(id)
}
func (s *Session) ResumeAgent(id identity.ActorID) (conversation.AgentInfo, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.AgentInfo{}, err
	}
	defer done()
	return s.controller.ResumeAgent(id)
}
func (s *Session) StopAgent(id identity.ActorID) (conversation.AgentInfo, error) {
	_, done, err := s.admission.Begin(context.Background())
	if err != nil {
		return conversation.AgentInfo{}, err
	}
	defer done()
	return s.controller.StopAgent(id)
}
func (s *Session) Receipt(id message.MessageID) (message.Receipt, bool) {
	return s.controller.Receipt(id)
}
func (s *Session) CountAgentTokens(ctx context.Context, id identity.ActorID, revision uint64) (int64, error) {
	run, done, err := s.admission.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	return s.controller.CountAgentTokens(run, id, revision)
}

// NextEvent is the transitional single-reader adapter used by the TUI until
// independent subscriptions replace the relay in the event-storage stage.
func (s *Session) NextEvent(ctx context.Context) (conversation.Event, error) {
	return s.workflow.NextEvent(ctx)
}

func localTools(dir string) ([]tool.Tool, error) {
	shell, err := tool.NewShell(tool.ShellConfig{Dir: dir})
	if err != nil {
		return nil, err
	}
	files, err := tool.NewFiles(tool.FilesConfig{Dir: dir})
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
	c.Model = cloneModel(c.Model)
	if c.Web != nil {
		v := *c.Web
		c.Web = &v
	}
	for _, r := range []*AgentConfig{&c.Root, &c.Implementor, &c.Auditor} {
		r.Prompt = r.Prompt.Clone()
		if r.Model != nil {
			v := cloneModel(*r.Model)
			r.Model = &v
		}
	}
	return c
}
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func cloneModel(m ModelConfig) ModelConfig {
	g := &m.Generation
	g.Temperature = copyPtr(g.Temperature)
	g.TopP = copyPtr(g.TopP)
	g.TopK = copyPtr(g.TopK)
	g.MinP = copyPtr(g.MinP)
	g.PresencePenalty = copyPtr(g.PresencePenalty)
	g.RepetitionPenalty = copyPtr(g.RepetitionPenalty)
	g.MaxTokens = copyPtr(g.MaxTokens)
	g.EnableThinking = copyPtr(g.EnableThinking)
	return m
}
