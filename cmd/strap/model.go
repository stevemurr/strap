package main

import (
	"cmp"
	"flag"
	"fmt"
	"net/http"
	"strconv"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/provider/vllm"
)

// Model policy lives at the composition root. Adapters never infer a preset
// from a model alias, an agent role, or the content of a request.
type modelOptions struct {
	backend, preset string
	overrides       vllm.Generation
}

func modelFlags(flags *flag.FlagSet) *modelOptions {
	o := &modelOptions{}
	flags.StringVar(&o.backend, "backend", "vllm", "Model backend: vllm or chatcompletions")
	flags.StringVar(&o.preset, "preset", "", "Generation preset: qwen3.6-coding or none (vllm defaults to qwen3.6-coding; chatcompletions to none)")
	floatFlag := func(name, help string, target **float64) {
		flags.Func(name, help, func(raw string) error {
			v, err := strconv.ParseFloat(raw, 64)
			if err == nil {
				*target = &v
			}
			return err
		})
	}
	intFlag := func(name, help string, target **int) {
		flags.Func(name, help, func(raw string) error {
			v, err := strconv.Atoi(raw)
			if err == nil {
				*target = &v
			}
			return err
		})
	}
	floatFlag("temperature", "Override vLLM temperature [0, 2]", &o.overrides.Temperature)
	floatFlag("top-p", "Override vLLM top-p (0, 1]", &o.overrides.TopP)
	intFlag("top-k", "Override vLLM top-k (-1 or 0 disables filtering)", &o.overrides.TopK)
	floatFlag("min-p", "Override vLLM min-p [0, 1]", &o.overrides.MinP)
	floatFlag("presence-penalty", "Override vLLM presence penalty [-2, 2]", &o.overrides.PresencePenalty)
	floatFlag("repetition-penalty", "Override vLLM repetition penalty (> 0)", &o.overrides.RepetitionPenalty)
	intFlag("max-tokens", "Override vLLM output token budget (> 0)", &o.overrides.MaxTokens)
	flags.BoolFunc("thinking", "Override vLLM thinking mode (-thinking=false disables it)", func(raw string) error {
		v, err := strconv.ParseBool(raw)
		if err == nil {
			o.overrides.EnableThinking = &v
		}
		return err
	})
	return o
}

func (o *modelOptions) newProvider(baseURL, model string, httpClient *http.Client) (provider.Provider, error) {
	switch o.backend {
	case "chatcompletions":
		if (o.preset != "" && o.preset != "none") || o.overrides != (vllm.Generation{}) {
			return nil, fmt.Errorf("generation presets and overrides require -backend vllm; chatcompletions uses server defaults")
		}
		return chatcompletions.New(chatcompletions.Config{BaseURL: baseURL, Model: model, HTTPClient: httpClient})
	case "vllm":
		var g vllm.Generation
		switch o.preset {
		case "", "qwen3.6-coding":
			g = qwenCodingPreset()
		case "none":
		default:
			return nil, fmt.Errorf("unknown generation preset %q; choose qwen3.6-coding or none", o.preset)
		}
		// These fields must stay pointers: cmp.Or picks the first non-nil, so an
		// explicit 0 or false still overrides the preset (DESIGN.md:152).
		g.Temperature = cmp.Or(o.overrides.Temperature, g.Temperature)
		g.TopP = cmp.Or(o.overrides.TopP, g.TopP)
		g.TopK = cmp.Or(o.overrides.TopK, g.TopK)
		g.MinP = cmp.Or(o.overrides.MinP, g.MinP)
		g.PresencePenalty = cmp.Or(o.overrides.PresencePenalty, g.PresencePenalty)
		g.RepetitionPenalty = cmp.Or(o.overrides.RepetitionPenalty, g.RepetitionPenalty)
		g.MaxTokens = cmp.Or(o.overrides.MaxTokens, g.MaxTokens)
		g.EnableThinking = cmp.Or(o.overrides.EnableThinking, g.EnableThinking)
		return vllm.New(vllm.Config{BaseURL: baseURL, Model: model, HTTPClient: httpClient, Generation: g})
	default:
		return nil, fmt.Errorf("unknown backend %q; choose vllm or chatcompletions", o.backend)
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
