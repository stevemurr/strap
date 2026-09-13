package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider/vllm"
)

//go:embed models.json
var bundledModels []byte

type modelCatalog struct {
	Default string                  `json:"default"`
	Models  map[string]modelProfile `json:"models"`
}

type modelProfile struct {
	Backend    string          `json:"backend"`
	BaseURL    string          `json:"base_url"`
	Model      string          `json:"model"`
	Preset     string          `json:"preset"`
	Timeout    string          `json:"timeout"`
	Generation vllm.Generation `json:"generation"`
}

func loadModel(path, profile string, timeout time.Duration) (harness.ModelConfig, error) {
	explicit := path != ""
	if !explicit {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return harness.ModelConfig{}, err
			}
			dir = filepath.Join(home, ".config")
		}
		path = filepath.Join(dir, "strap", "models.json")
	}
	data, err := os.ReadFile(path)
	if !explicit && errors.Is(err, os.ErrNotExist) {
		data, err = bundledModels, nil
		path = "bundled models.json"
	}
	if err != nil {
		return harness.ModelConfig{}, fmt.Errorf("model catalog: %w", err)
	}
	var catalog modelCatalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return harness.ModelConfig{}, fmt.Errorf("model catalog %s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return harness.ModelConfig{}, fmt.Errorf("model catalog %s: expected one JSON object", path)
	}
	if profile == "" {
		profile = catalog.Default
	}
	p, ok := catalog.Models[profile]
	if !ok || profile == "" {
		return harness.ModelConfig{}, fmt.Errorf("model catalog %s: unknown profile %q; set default or use -profile", path, profile)
	}
	if p.Timeout != "" {
		timeout, err = time.ParseDuration(p.Timeout)
		if err != nil {
			return harness.ModelConfig{}, fmt.Errorf("profile %q timeout: %w", profile, err)
		}
	}
	if p.Backend == "" {
		p.Backend = "vllm"
	}
	if p.Preset == "" {
		p.Preset = "none"
	}
	return harness.ModelConfig{Backend: p.Backend, BaseURL: p.BaseURL, Model: p.Model, Preset: p.Preset, Timeout: timeout, Generation: p.Generation}, nil
}
