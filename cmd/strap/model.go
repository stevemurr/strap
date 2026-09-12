package main

import (
	"flag"
	"strconv"

	"github.com/stevemurr/strap/harness"
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

func (o *modelOptions) config(baseURL, model string) harness.ModelConfig {
	return harness.ModelConfig{Backend: o.backend, Preset: o.preset, BaseURL: baseURL, Model: model, Generation: o.overrides}
}
