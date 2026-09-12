package harness

import (
	"encoding/json"
	"net/url"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
)

type RoleConfiguration struct {
	Model            *ModelConfig              `json:"model,omitempty"`
	InjectedProvider bool                      `json:"injected_provider"`
	Prompt           prompt.Prompt             `json:"prompt"`
	Tools            []provider.ToolDefinition `json:"tools"`
}
type EffectiveConfig struct {
	Dir         string            `json:"dir"`
	Telemetry   TelemetryConfig   `json:"telemetry"`
	Events      EventConfig       `json:"events"`
	Root        RoleConfiguration `json:"root"`
	Implementor RoleConfiguration `json:"implementor"`
	Auditor     RoleConfiguration `json:"auditor"`
}

func describeRole(cfg Config, role AgentConfig, spec agent.Spec, injected bool) RoleConfiguration {
	r := RoleConfiguration{InjectedProvider: injected, Prompt: spec.Prompt.Clone()}
	if !injected {
		m := cfg.Model
		if role.Model != nil {
			m = *role.Model
		}
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
	return r
}

// Configuration returns the resolved construction configuration, excluding URL
// credentials. Injected providers are explicitly opaque; their settings are not
// inferred from the unused model defaults. Dynamic CreateAgent specs are separate.
func (s *Session) Configuration() EffectiveConfig {
	c := s.effective
	for _, r := range []*RoleConfiguration{&c.Root, &c.Implementor, &c.Auditor} {
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
	DomainEvents      bool `json:"domain_events"`
	ToolDiagnostics   bool `json:"tool_diagnostics"`
	ModelRequests     bool `json:"model_requests"`
	ModelResponses    bool `json:"model_responses"`
	ExternalArtifacts bool `json:"external_artifacts"`
}
type Inspection struct {
	ID       string          `json:"id"`
	State    State           `json:"state"`
	Capture  eventlog.Status `json:"capture"`
	Coverage Coverage        `json:"coverage"`
	Config   EffectiveConfig `json:"config"`
}

// Inspect is a collection of independent snapshots, not a global execution checkpoint.
func (s *Session) Inspect() Inspection {
	return Inspection{ID: s.ID(), State: s.State(), Capture: s.Capture(), Coverage: Coverage{DomainEvents: true, ToolDiagnostics: true}, Config: s.Configuration()}
}
