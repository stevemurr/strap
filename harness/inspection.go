package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type RoleConfiguration struct {
	SchemaHash       string                    `json:"schema_hash"`
	Model            *ModelConfig              `json:"model,omitempty"`
	InjectedProvider bool                      `json:"injected_provider"`
	Prompt           prompt.Prompt             `json:"prompt"`
	Tools            []provider.ToolDefinition `json:"tools"`
}
type EffectiveConfig struct {
	DeepResearch          DeepResearchConfig          `json:"deep_research"`
	LSP                   *LSPConfiguration           `json:"lsp,omitempty"`
	ToolContractVersion   string                      `json:"tool_contract_version"`
	WorkProgressReporting WorkProgressReportingConfig `json:"work_progress_reporting"`
	Dir                   string                      `json:"dir"`
	LocalTools            bool                        `json:"local_tools"`
	FileEdits             tool.EditMode               `json:"file_edits,omitempty"`
	Web                   *tool.WebConfig             `json:"web,omitempty"` // Credentials are never serialized.
	DebugToolkit          bool                        `json:"debug_toolkit,omitempty"`
	ReasoningLimit        int                         `json:"reasoning_limit"`
	Telemetry             TelemetryConfig             `json:"telemetry"`
	Events                EventConfig                 `json:"events"`
	Agent                 *RoleConfiguration          `json:"agent,omitempty"` // Present only in a solo session.
	Tester                bool                        `json:"tester,omitempty"`
	Manager               RoleConfiguration           `json:"manager"`
	Implementor           RoleConfiguration           `json:"implementor"`
	Auditor               RoleConfiguration           `json:"auditor"`
	WebResearcher         RoleConfiguration           `json:"web_researcher"`
	DeepResearcher        *RoleConfiguration          `json:"deep_researcher,omitempty"` // Present only when deep research is enabled.
	Experimenter          *RoleConfiguration          `json:"experimenter,omitempty"`    // Present only with local tools.
	Reviewer              *RoleConfiguration          `json:"reviewer,omitempty"`        // Present only with local tools.
	Debugger              *RoleConfiguration          `json:"debugger,omitempty"`
}

// Roles lists every configured role, the entry agent first.
func (c EffectiveConfig) Roles() []RoleConfiguration {
	var roles []RoleConfiguration
	if c.Agent != nil {
		roles = append(roles, *c.Agent)
	}
	roles = append(roles, c.Manager, c.Implementor, c.Auditor, c.WebResearcher)
	if c.DeepResearcher != nil {
		roles = append(roles, *c.DeepResearcher)
	}
	if c.Experimenter != nil {
		roles = append(roles, *c.Experimenter)
	}
	if c.Reviewer != nil {
		roles = append(roles, *c.Reviewer)
	}
	return roles
}

func describeRole(m ModelConfig, spec agent.Spec, injected bool) RoleConfiguration {
	r := RoleConfiguration{InjectedProvider: injected, Prompt: spec.Prompt.Clone()}
	if !injected {
		resolved, _ := m.Resolve()
		if u, err := url.Parse(resolved.BaseURL); err == nil {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			resolved.BaseURL = u.String()
		}
		r.Model = &resolved
	}
	for _, t := range spec.Tools {
		d := t.Definition()
		d.Parameters = append(json.RawMessage(nil), d.Parameters...)
		r.Tools = append(r.Tools, d)
	}
	// Hash canonical names and schemas independently of registration order.
	contracts := append([]provider.ToolDefinition(nil), r.Tools...)
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].Name < contracts[j].Name })
	for i := range contracts {
		contracts[i].Description = ""
	}
	raw, _ := json.Marshal(contracts)
	digest := sha256.Sum256(raw)
	r.SchemaHash = hex.EncodeToString(digest[:])
	return r
}

// Configuration returns the resolved construction configuration, excluding URL
// credentials. Injected providers are explicitly opaque; their settings are not
// inferred from the unused model defaults. Dynamic CreateAgent specs are separate.
func (s *Session) Configuration() EffectiveConfig {
	c := s.effective
	if c.DeepResearch.Model != nil {
		m := cloneModel(*c.DeepResearch.Model)
		c.DeepResearch.Model = &m
	}
	if c.LSP != nil {
		v := *c.LSP
		v.Servers = append([]string(nil), v.Servers...)
		c.LSP = &v
	}
	if c.Web != nil {
		w := *c.Web
		c.Web = &w
	}
	roles := []*RoleConfiguration{&c.Manager, &c.Implementor, &c.Auditor, &c.WebResearcher}
	if c.Agent != nil {
		a := *c.Agent
		c.Agent = &a
		roles = append(roles, c.Agent)
	}
	if c.DeepResearcher != nil {
		d := *c.DeepResearcher
		c.DeepResearcher = &d
		roles = append(roles, c.DeepResearcher)
	}
	if c.Experimenter != nil {
		e := *c.Experimenter
		c.Experimenter = &e
		roles = append(roles, c.Experimenter)
	}
	if c.Reviewer != nil {
		r := *c.Reviewer
		c.Reviewer = &r
		roles = append(roles, c.Reviewer)
	}
	if c.Debugger != nil {
		d := *c.Debugger
		c.Debugger = &d
		roles = append(roles, c.Debugger)
	}
	for _, r := range roles {
		r.Prompt = r.Prompt.Clone()
		if r.Model != nil {
			m := cloneModel(*r.Model)
			r.Model = &m
		}
		r.Tools = append([]provider.ToolDefinition(nil), r.Tools...)
		for i := range r.Tools {
			r.Tools[i].Parameters = append(json.RawMessage(nil), r.Tools[i].Parameters...)
		}
	}
	return c
}

// Coverage describes instrumentation, independently of storage retention or health.
type Coverage struct {
	ModelHistory    bool `json:"model_history"`
	StreamingOutput bool `json:"streaming_output"`
	DomainEvents    bool `json:"domain_events"`
	ToolDiagnostics bool `json:"tool_diagnostics"`
}
type Inspection struct {
	Outcome  *eventlog.Outcome `json:"outcome,omitempty"`
	ID       string            `json:"id"`
	State    State             `json:"state"`
	Capture  eventlog.Status   `json:"capture"`
	Coverage Coverage          `json:"coverage"`
	Config   EffectiveConfig   `json:"config"`
}

// Inspect is a collection of independent snapshots, not a global execution checkpoint.
func (s *Session) Inspect() Inspection {
	s.mu.Lock()
	var outcome *eventlog.Outcome
	if s.outcome != nil {
		v := *s.outcome
		outcome = &v
	}
	s.mu.Unlock()
	return Inspection{Outcome: outcome, ID: s.ID(), State: s.State(), Capture: s.Capture(), Coverage: Coverage{DomainEvents: true, ToolDiagnostics: true, ModelHistory: true, StreamingOutput: true}, Config: s.Configuration()}
}

// LSPConfiguration identifies the immutable server configuration without exposing
// environment values, command arguments, or arbitrary initialization settings.
type LSPConfiguration struct {
	Experimental bool     `json:"experimental"`
	Servers      []string `json:"servers"`
	Fingerprint  string   `json:"fingerprint"`
}

func describeLSP(cfg *lsp.Config) *LSPConfiguration {
	if cfg == nil {
		return nil
	}
	raw, _ := json.Marshal(cfg)
	sum := sha256.Sum256(raw)
	out := &LSPConfiguration{Experimental: true, Fingerprint: hex.EncodeToString(sum[:])}
	for _, server := range cfg.Servers {
		out.Servers = append(out.Servers, server.ID)
	}
	return out
}
