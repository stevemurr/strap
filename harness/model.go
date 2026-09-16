package harness

import (
	"fmt"
	"net/http"
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/provider/vllm"
)

// ModelConfig is shared by CLI and library hosts. Generation is the complete
// sampling policy for the endpoint; there are no named presets, so a profile
// in the model catalog states every setting it wants and the rest stay at
// the server's defaults.
type ModelConfig struct {
	Backend    string          `json:"backend"`
	BaseURL    string          `json:"base_url"`
	Model      string          `json:"model"`
	Timeout    time.Duration   `json:"timeout_ns"`
	Generation vllm.Generation `json:"generation"`
}

// NewProvider borrows the supplied HTTP client. Sessions supply their owned transport.
func (o ModelConfig) NewProvider(httpClient *http.Client) (provider.Provider, error) {
	resolved, err := o.Resolve()
	if err != nil {
		return nil, err
	}
	if resolved.Backend == "chatcompletions" {
		return chatcompletions.New(chatcompletions.Config{BaseURL: resolved.BaseURL, Model: resolved.Model, HTTPClient: httpClient})
	}
	return vllm.New(vllm.Config{BaseURL: resolved.BaseURL, Model: resolved.Model, HTTPClient: httpClient, Generation: resolved.Generation})
}

// Resolve validates the backend and returns an independent copy without
// opening connections. Generation carries the complete sampling policy as
// given: the CLI reads it from the model catalog and applies flag overrides,
// and nothing in the harness selects settings by name.
func (o ModelConfig) Resolve() (ModelConfig, error) {
	switch o.Backend {
	case "chatcompletions":
		if o.Generation != (vllm.Generation{}) {
			return ModelConfig{}, fmt.Errorf("generation settings require -backend vllm; chatcompletions uses server defaults")
		}
		return cloneModel(o), nil
	case "", "vllm":
		o.Backend = "vllm"
		return cloneModel(o), nil
	default:
		return ModelConfig{}, fmt.Errorf("unknown backend %q; choose vllm or chatcompletions", o.Backend)
	}
}
