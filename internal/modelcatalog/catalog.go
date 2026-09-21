// Package modelcatalog loads saved model profiles and registers the model flags
// shared by the strap command-line programs.
package modelcatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider/vllm"
)

//go:embed models.json
var bundled []byte

type catalog struct {
	Default string             `json:"default"`
	Models  map[string]profile `json:"models"`
}

type profile struct {
	Backend    string          `json:"backend"`
	BaseURL    string          `json:"base_url"`
	Model      string          `json:"model"`
	Timeout    string          `json:"timeout"`
	Generation vllm.Generation `json:"generation"`
}

// Resolve resolves a model profile and returns the selected profile's name.
// An empty path discovers $XDG_CONFIG_HOME/strap/models.json or
// ~/.config/strap/models.json and falls back to the bundled catalog; an
// explicit path must exist. An empty profile selects the catalog default. The
// eval runner names run directories by the profile name: a model id alone
// cannot tell apart profiles that share a model, such as thinking and
// no-thinking variants.
func Resolve(path, profile string, timeout time.Duration) (harness.ModelConfig, string, error) {
	explicit := path != ""
	if !explicit {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return harness.ModelConfig{}, "", err
			}
			dir = filepath.Join(home, ".config")
		}
		path = filepath.Join(dir, "strap", "models.json")
	}
	data, err := os.ReadFile(path)
	if !explicit && errors.Is(err, os.ErrNotExist) {
		data, err = bundled, nil
		path = "bundled models.json"
	}
	if err != nil {
		return harness.ModelConfig{}, "", fmt.Errorf("model catalog: %w", err)
	}
	var c catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return harness.ModelConfig{}, "", fmt.Errorf("model catalog %s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return harness.ModelConfig{}, "", fmt.Errorf("model catalog %s: expected one JSON object", path)
	}
	if profile == "" {
		profile = c.Default
	}
	p, ok := c.Models[profile]
	if !ok || profile == "" {
		return harness.ModelConfig{}, "", fmt.Errorf("model catalog %s: unknown profile %q; set default or use -profile", path, profile)
	}
	if p.Timeout != "" {
		timeout, err = time.ParseDuration(p.Timeout)
		if err != nil {
			return harness.ModelConfig{}, "", fmt.Errorf("profile %q timeout: %w", profile, err)
		}
	}
	if p.Backend == "" {
		p.Backend = "vllm"
	}
	return harness.ModelConfig{Backend: p.Backend, BaseURL: p.BaseURL, Model: p.Model, Timeout: timeout, Generation: p.Generation}, profile, nil
}

// Flags registers the model override flags. Each overrides one setting of the
// selected profile; the profile itself carries the complete generation policy.
func Flags(flags *flag.FlagSet, model *harness.ModelConfig) {
	flags.StringVar(&model.BaseURL, "base-url", model.BaseURL, "Local server root or API prefix")
	flags.StringVar(&model.Model, "model", model.Model, "Model served by the endpoint (does not select a profile)")
	flags.DurationVar(&model.Timeout, "timeout", model.Timeout, "Timeout for each model HTTP request")
	flags.StringVar(&model.Backend, "backend", model.Backend, "Model backend: vllm or chatcompletions")
	flags.Func("reasoning-effort", "Override Qwen template reasoning effort: low, medium, or xhigh", func(raw string) error {
		model.Generation.ReasoningEffort = &raw
		return nil
	})
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

}

// Apply resolves the selected profile into model, then reparses args so every
// flag registered by Flags overrides the profile. It returns the profile name.
// Call it after the first Parse, which discovered the catalog and profile.
func Apply(flags *flag.FlagSet, args []string, model *harness.ModelConfig, path, profile string) (string, error) {
	resolved, name, err := Resolve(path, profile, model.Timeout)
	if err != nil {
		return "", err
	}
	if WasSet(flags, "backend") && model.Backend == "chatcompletions" {
		// The generic backend uses server defaults unless flags explicitly override.
		resolved.Generation = vllm.Generation{}
	}
	*model = resolved
	return name, flags.Parse(args)
}

// WasSet reports whether a flag was given explicitly on the command line.
func WasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
