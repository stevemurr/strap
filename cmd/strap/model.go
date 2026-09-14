package main

import (
	"flag"
	"strconv"

	"github.com/stevemurr/strap/harness"
)

// Model aliases never select a preset; profiles select policy explicitly.
func modelFlags(flags *flag.FlagSet, model *harness.ModelConfig) {
	flags.StringVar(&model.BaseURL, "base-url", model.BaseURL, "Local server root or API prefix")
	flags.StringVar(&model.Model, "model", model.Model, "Model served by the endpoint (does not select a profile)")
	flags.DurationVar(&model.Timeout, "timeout", model.Timeout, "Timeout for each model HTTP request")
	flags.StringVar(&model.Backend, "backend", model.Backend, "Model backend: vllm or chatcompletions")
	flags.StringVar(&model.Preset, "preset", model.Preset, "Replace saved generation settings with qwen3.6-coding or none")
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
	floatFlag("temperature", "Override vLLM temperature [0, 2]", &model.Generation.Temperature)
	floatFlag("top-p", "Override vLLM top-p (0, 1]", &model.Generation.TopP)
	intFlag("top-k", "Override vLLM top-k (-1 or 0 disables filtering)", &model.Generation.TopK)
	floatFlag("min-p", "Override vLLM min-p [0, 1]", &model.Generation.MinP)
	floatFlag("presence-penalty", "Override vLLM presence penalty [-2, 2]", &model.Generation.PresencePenalty)
	floatFlag("repetition-penalty", "Override vLLM repetition penalty (> 0)", &model.Generation.RepetitionPenalty)
	intFlag("max-tokens", "Override vLLM output token budget (> 0)", &model.Generation.MaxTokens)
	boolFlag := func(name, help string, target **bool) {
		flags.BoolFunc(name, help, func(raw string) error {
			v, err := strconv.ParseBool(raw)
			if err == nil {
				*target = &v
			}
			return err
		})
	}
	boolFlag("thinking", "Override thinking mode (-thinking=false disables it)", &model.Generation.EnableThinking)
	boolFlag("force-nonempty-content", "Require assistant content with tool calls (requires chat-template support)", &model.Generation.ForceNonemptyContent)
	boolFlag("strict-tools", "Constrain vLLM tool-call generation to each tool's schema (structural tags; test per tool before enabling in a profile)", &model.Generation.StrictTools)
}
