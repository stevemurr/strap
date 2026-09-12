package harness

import (
	"cmp"
	"fmt"
	"net/http"
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/provider/vllm"
)

// ModelConfig is shared by CLI and library hosts. The model name never selects a preset.
type ModelConfig struct {
	Backend    string
	Preset     string
	BaseURL    string
	Model      string
	Timeout    time.Duration
	Generation vllm.Generation
}

// NewProvider borrows the supplied HTTP client. Sessions supply their owned transport.
func (o ModelConfig) NewProvider(httpClient *http.Client) (provider.Provider, error) {
	switch o.Backend {
	case "chatcompletions":
		if (o.Preset != "" && o.Preset != "none") || o.Generation != (vllm.Generation{}) {
			return nil, fmt.Errorf("generation presets and overrides require -backend vllm; chatcompletions uses server defaults")
		}
		return chatcompletions.New(chatcompletions.Config{BaseURL: o.BaseURL, Model: o.Model, HTTPClient: httpClient})
	case "", "vllm":
		var g vllm.Generation
		switch o.Preset {
		case "", "qwen3.6-coding":
			g = qwenCodingPreset()
		case "none":
		default:
			return nil, fmt.Errorf("unknown generation preset %q; choose qwen3.6-coding or none", o.Preset)
		}
		// These fields must stay pointers: cmp.Or picks the first non-nil, so an
		// explicit 0 or false still overrides the preset (DESIGN.md:152).
		g.Temperature = cmp.Or(o.Generation.Temperature, g.Temperature)
		g.TopP = cmp.Or(o.Generation.TopP, g.TopP)
		g.TopK = cmp.Or(o.Generation.TopK, g.TopK)
		g.MinP = cmp.Or(o.Generation.MinP, g.MinP)
		g.PresencePenalty = cmp.Or(o.Generation.PresencePenalty, g.PresencePenalty)
		g.RepetitionPenalty = cmp.Or(o.Generation.RepetitionPenalty, g.RepetitionPenalty)
		g.MaxTokens = cmp.Or(o.Generation.MaxTokens, g.MaxTokens)
		g.EnableThinking = cmp.Or(o.Generation.EnableThinking, g.EnableThinking)
		return vllm.New(vllm.Config{BaseURL: o.BaseURL, Model: o.Model, HTTPClient: httpClient, Generation: g})
	default:
		return nil, fmt.Errorf("unknown backend %q; choose vllm or chatcompletions", o.Backend)
	}
}

// Qwen's precise-coding thinking preset and general output budget, checked
// 2026-09-11: https://huggingface.co/Qwen/Qwen3.6-35B-A3B#best-practices
// Fresh pointers keep presets independent even before the adapter snapshots them.
func qwenCodingPreset() vllm.Generation {
	return vllm.Generation{
		Temperature: valuePtr(0.6), TopP: valuePtr(0.95), TopK: valuePtr(20),
		MinP: valuePtr(0.0), PresencePenalty: valuePtr(0.0), RepetitionPenalty: valuePtr(1.0),
		MaxTokens: valuePtr(32768), EnableThinking: valuePtr(true),
	}
}

func valuePtr[T any](v T) *T { return &v }
