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
	ResearchExecution     ResearchExecutionConfig     `json:"research_execution"`
	WorkProgressReporting WorkProgressReportingConfig `json:"work_progress_reporting"`
	Dir                   string                      `json:"dir"`
	ReasoningLimit        int                         `json:"reasoning_limit"`
	Telemetry             TelemetryConfig             `json:"telemetry"`
	Events                EventConfig                 `json:"events"`
	Root                  RoleConfiguration           `json:"root"`
	Implementor           RoleConfiguration           `json:"implementor"`
	Auditor               RoleConfiguration           `json:"auditor"`
	Researcher            RoleConfiguration           `json:"researcher"`
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
	c.ResearchExecution.Env = append([]string(nil), c.ResearchExecution.Env...)
	for _, r := range []*RoleConfiguration{&c.Root, &c.Implementor, &c.Auditor, &c.Researcher} {
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
