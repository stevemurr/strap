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
	// StallFirstChunk and StallIdle bound how long a response may deliver
	// nothing before the call is abandoned and repeated. They differ because
	// the waits differ in kind: a prompt still being read can be slow and
	// grows with context, while a stream that has started should never pause
	// for long.
	//
	// Zero takes the default rather than disabling the watchdog. A guard that
	// every caller must remember to copy is a guard that silently goes missing
	// when one of them builds a config from parts; a negative value disables
	// it, which is a choice a reader can see.
	StallFirstChunk time.Duration `json:"stall_first_chunk_ns"`
	StallIdle       time.Duration `json:"stall_idle_ns"`
}

// NewProvider borrows the supplied HTTP client. Sessions supply their owned transport.
func (o ModelConfig) NewProvider(httpClient *http.Client) (provider.Provider, error) {
	resolved, err := o.Resolve()
	if err != nil {
		return nil, err
	}
	stall := vllm.StallPolicy{FirstChunk: max(resolved.StallFirstChunk, 0), Idle: max(resolved.StallIdle, 0)}
	if resolved.Backend == "chatcompletions" {
		return chatcompletions.New(chatcompletions.Config{BaseURL: resolved.BaseURL, Model: resolved.Model, HTTPClient: httpClient, Stall: stall})
	}
	return vllm.New(vllm.Config{BaseURL: resolved.BaseURL, Model: resolved.Model, HTTPClient: httpClient, Generation: resolved.Generation, Stall: stall})
}

// Resolve validates the backend and returns an independent copy without
// opening connections. Generation carries the complete sampling policy as
// given: the CLI reads it from the model catalog and applies flag overrides,
// and nothing in the harness selects settings by name.
// DefaultStallFirstChunk and DefaultStallIdle bound a silent response. The
// first is generous because reading a long prompt is legitimate work; the
// second is not, because recorded generation has never paused beyond about two
// seconds once a stream has started.
const (
	DefaultStallFirstChunk = 2 * time.Minute
	DefaultStallIdle       = 25 * time.Second
)

func (o ModelConfig) Resolve() (ModelConfig, error) {
	if o.StallFirstChunk == 0 {
		o.StallFirstChunk = DefaultStallFirstChunk
	}
	if o.StallIdle == 0 {
		o.StallIdle = DefaultStallIdle
	}
	switch o.Backend {
	case "chatcompletions":
		if !o.Generation.Empty() {
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
