package modelcatalog

import (
	"testing"
	"time"
)

// Every profile states its full generation policy in the catalog; nothing is
// filled in by name at load time.
func TestBundledProfilesCarryTheirGenerationSettings(t *testing.T) {
	cases := map[string]struct {
		model, baseURL string
		maxTokens      int
		thinking       bool
		effort         string
	}{
		"qwen3.6":                     {"qwen3.6", "http://192.168.1.237:8355", 131072, true, ""},
		"qwen3.6-nothink":             {"qwen3.6", "http://192.168.1.237:8355", 131072, false, ""},
		"qwen3.8-flash-next-nothink":  {"qwen3.8-flash-next-mtp3", "http://192.168.1.237:8365", 32000, false, ""},
		"qwen3.8-flash-next-thinking": {"qwen3.8-flash-next-mtp3", "http://192.168.1.237:8365", 32000, true, "medium"},
		"qwen3.8-flash-next-stream":   {"qwen3.8-flash-next-mtp3", "http://192.168.1.237:8365", 400, false, ""},
		"qwen3.8-27b":                 {"qwen3.8-27b", "http://192.168.1.237:8360", 131072, true, "xhigh"},
		"qwen3.8-27b-nothink":         {"qwen3.8-27b", "http://192.168.1.237:8360", 131072, false, ""},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			model, profile, err := Resolve("models.json", name, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			g := model.Generation
			if profile != name || model.Backend != "vllm" || model.Model != want.model || model.BaseURL != want.baseURL || model.Timeout != time.Hour || g.MaxTokens == nil || *g.MaxTokens != want.maxTokens || g.EnableThinking == nil || *g.EnableThinking != want.thinking {
				t.Fatalf("incorrect profile: %+v", model)
			}
			if (want.effort == "") != (g.ReasoningEffort == nil) || (g.ReasoningEffort != nil && *g.ReasoningEffort != want.effort) {
				t.Fatalf("reasoning effort: %v", g.ReasoningEffort)
			}
			if _, err := model.NewProvider(nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := Load("models.json", "nemotron-lightning", time.Second); err != nil {
		t.Fatal(err)
	}
}
