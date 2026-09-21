package tool

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

// This opt-in probe qualifies schema acceptance and first-call validity only.
// Real language-server behavior is tested independently in lsp; this is not a
// task-success benchmark or evidence that a model traverses code effectively.
func TestLiveLanguageSchemas(t *testing.T) {
	base, model := os.Getenv("STRAP_LIVE_BASE_URL"), os.Getenv("STRAP_LIVE_MODEL")
	if os.Getenv("STRAP_LIVE_LSP") != "1" || base == "" || model == "" {
		t.Skip("set STRAP_LIVE_LSP=1, STRAP_LIVE_BASE_URL and STRAP_LIVE_MODEL")
	}
	maxTokens := 1024
	temperature := 0.0
	client, err := vllm.New(vllm.Config{BaseURL: base, Model: model, Generation: vllm.Generation{MaxTokens: &maxTokens, Temperature: &temperature}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := LSPTools(&languageSpy{})
	if err != nil {
		t.Fatal(err)
	}
	definitions := []provider.ToolDefinition{}
	byName := map[string]Tool{}
	for _, tool := range tools {
		definition := tool.Definition()
		definitions = append(definitions, definition)
		byName[tool.Definition().Name] = tool
	}
	for _, tc := range []struct{ name, prompt, expected string }{
		{"lsp_status", "Check language server status. Do not filter by path.", `{"path":null}`},
		{"lsp_symbols", "Find workspace symbols named Alpha, without a path filter.", `{"query":"Alpha","path":null}`},
		{"lsp_outline", "Get the outline of fixture.go at depth 1.", `{"path":"fixture.go","depth":1}`},
		{"lsp_inspect", "Inspect the existing reference loc_example_1, including its source.", `{"target_kind":"reference","ref":"loc_example_1","include_source":true}`},
		{"lsp_inspect", "Inspect the identifier Alpha on line 2 of fixture.go, including source. It occurs once on that line.", `{"target_kind":"symbol","path":"fixture.go","line":2,"symbol":"Alpha","context":null,"include_source":true}`},
		{"lsp_navigate", "Find the definition of existing reference loc_example_1.", `{"target_kind":"reference","ref":"loc_example_1","relation":"definition"}`},
		{"lsp_references", "Find references to existing reference loc_example_1, including the declaration.", `{"target_kind":"reference","ref":"loc_example_1","include_declaration":true}`},
		{"lsp_diagnostics", "Check current diagnostics for fixture.go.", `{"paths":["fixture.go"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			start := time.Now()
			response, err := client.Submit(ctx, provider.Request{Tools: definitions, Messages: []provider.Message{
				{Role: "system", Content: content.Text("Use exactly one requested tool now. All tool arguments are under input. Every declared field must be present. Use null for default limit, cursor and optional values. Do not answer with prose.")},
				{Role: "user", Content: content.Text(tc.prompt)},
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			measurement, _ := json.Marshal(map[string]any{"model": model, "latency_ms": time.Since(start).Milliseconds(), "usage": response.Usage, "calls": response.ToolCalls, "reasoning_bytes": len(response.Reasoning), "descriptions": "shipped"})
			t.Log(string(measurement))
			if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != tc.name {
				t.Fatalf("expected one %s call: %+v", tc.name, response.ToolCalls)
			}
			call := response.ToolCalls[0]
			if err := ValidateArguments(byName[tc.name], call.Arguments); err != nil {
				t.Fatalf("invalid first call %s: %v", call.Arguments, err)
			}
			var expected map[string]any
			var actual struct {
				Input map[string]any `json:"input"`
			}
			if err := json.Unmarshal([]byte(tc.expected), &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(call.Arguments, &actual); err != nil {
				t.Fatal(err)
			}
			for key, value := range expected {
				if got, ok := actual.Input[key]; !ok || !reflect.DeepEqual(got, value) {
					t.Errorf("%s: got %v, want %v", key, got, value)
				}
			}
		})
	}
}
