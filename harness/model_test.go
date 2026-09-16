package harness_test

import (
	"strings"
	"testing"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider/vllm"
)

// Resolve validates the backend and copies the generation policy as given;
// nothing is filled in by name.
func TestModelConfigResolvePassesGenerationThrough(t *testing.T) {
	temperature, thinking := 0.3, false
	cfg := harness.ModelConfig{BaseURL: "http://model.test", Model: "m", Generation: vllm.Generation{Temperature: &temperature, EnableThinking: &thinking}}
	resolved, err := cfg.Resolve()
	if err != nil || resolved.Backend != "vllm" || resolved.Generation.Temperature == nil || *resolved.Generation.Temperature != 0.3 || resolved.Generation.MaxTokens != nil || resolved.Generation.TopP != nil {
		t.Fatalf("%+v %v", resolved, err)
	}
	*resolved.Generation.Temperature = 0.9
	if *cfg.Generation.Temperature != 0.3 {
		t.Fatal("resolved config aliases the caller's values")
	}
	generic := harness.ModelConfig{Backend: "chatcompletions", Generation: vllm.Generation{Temperature: &temperature}}
	if _, err := generic.Resolve(); err == nil || !strings.Contains(err.Error(), "server defaults") {
		t.Fatalf("chatcompletions with generation settings: %v", err)
	}
	unknown := harness.ModelConfig{Backend: "other"}
	if _, err := unknown.Resolve(); err == nil {
		t.Fatal("unknown backend accepted")
	}
}
