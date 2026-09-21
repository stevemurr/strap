package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

// Preserve the pre-qualification wording for comparisons against the current
// schema. Schemas, tasks, sampling and prompts stay identical within each A/B
// pair. Historical measurements used the former position branch; rerunning this
// probe after the symbol-target change is not an exact historical reproduction.
var languageOriginalDescriptions = map[string]string{
	"lsp_symbols":    "Find workspace declarations by name. Returns reusable refs and small source excerpts. Null path searches configured/discovered roots. Results reflect server build/index scope; use shell search for literal text and unsupported languages. Follow cursor with the same arguments.",
	"lsp_inspect":    "Inspect types, documentation and source at a returned ref or explicit position. References expire when their file changes; rediscover stale references. Lines/columns are 1-based Unicode code points. Source defaults to included, bounded to 80 lines.",
	"lsp_navigate":   "Follow a symbol's definition, declaration, type definition or implementations. Use a returned ref or a 1-based source position. Returns locations with source excerpts and new refs. Unsupported capabilities are errors, not empty results.",
	"lsp_references": "Find semantic usages of a symbol, with source excerpts and reusable location refs. Excludes its declaration by default. Scope depends on the server's workspace/build; strings, reflection and other build targets may require text search. Follow cursor with unchanged arguments.",
}

var languageDescriptionCandidates = map[string]string{
	"lsp_symbols":    " Search source symbol names, never location handles. A loc_ value is an opaque handle, not a symbol name. Pass an existing handle directly to lsp_inspect, lsp_navigate or lsp_references as ref; do not search for it with this tool.",
	"lsp_inspect":    " An existing loc_ handle needs no discovery step: set input.target_kind to reference and input.ref to the unchanged handle.",
	"lsp_navigate":   " To find a definition from an existing loc_ handle, call this tool directly with input.target_kind=reference, input.ref equal to the unchanged handle, and input.relation=definition. Supply limit and cursor as null for defaults. A handle is not a symbol name; do not search for it with lsp_symbols.",
	"lsp_references": " To find usages from an existing loc_ handle, call this tool directly with input.target_kind=reference and input.ref equal to the unchanged handle. Set include_declaration as requested; supply limit and cursor as null for defaults. A handle is not a symbol name; do not search for it with lsp_symbols.",
}

type languageDescriptionMeasurement struct {
	Context        string              `json:"context"`
	Variant        string              `json:"variant"`
	Expected       string              `json:"expected"`
	Trial          int                 `json:"trial"`
	Calls          []provider.ToolCall `json:"calls"`
	Selected       bool                `json:"selected"`
	ValidArguments bool                `json:"valid_arguments"`
	CorrectIntent  bool                `json:"correct_intent"`
	LatencyMS      int64               `json:"latency_ms"`
	Usage          *provider.Usage     `json:"usage"`
	ReasoningBytes int                 `json:"reasoning_bytes"`
	Error          string              `json:"error,omitempty"`
}

func TestLiveLanguageDescriptions(t *testing.T) {
	base, model := os.Getenv("STRAP_LIVE_BASE_URL"), os.Getenv("STRAP_LIVE_MODEL")
	if os.Getenv("STRAP_LIVE_LSP_AB") != "1" || base == "" || model == "" {
		t.Skip("set STRAP_LIVE_LSP_AB=1, STRAP_LIVE_BASE_URL and STRAP_LIVE_MODEL")
	}
	maxTokens, temperature := 1024, 0.0
	client, err := vllm.New(vllm.Config{BaseURL: base, Model: model, Generation: vllm.Generation{MaxTokens: &maxTokens, Temperature: &temperature}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := LSPTools(&languageSpy{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Tool{}
	for _, item := range tools {
		byName[item.Definition().Name] = item
	}
	output := os.Getenv("STRAP_LIVE_LSP_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	var measurements []languageDescriptionMeasurement
	defer func() {
		data, _ := json.MarshalIndent(map[string]any{"model": model, "temperature": temperature, "max_tokens": maxTokens, "thinking": "server default", "candidate_suffixes": languageDescriptionCandidates, "measurements": measurements}, "", "  ")
		path := filepath.Join(output, "descriptions-"+time.Now().UTC().Format("20060102T150405.000000000")+".json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Error(err)
		}
		t.Log("measurements:", path)
	}()
	for _, history := range []string{"isolated", "receipt"} {
		for _, tc := range []struct{ name, prompt string }{
			{"lsp_navigate", "Find the definition of existing reference loc_example_1."},
			{"lsp_references", "Find references to existing reference loc_example_1, including the declaration."},
		} {
			for trial := 1; trial <= 3; trial++ {
				variants := []string{"original", "clarified"}
				if trial%2 == 0 {
					variants[0], variants[1] = variants[1], variants[0]
				}
				for _, variant := range variants {
					t.Run(fmt.Sprintf("%s/%s/%d/%s", history, tc.name, trial, variant), func(t *testing.T) {
						definitions := make([]provider.ToolDefinition, 0, len(tools))
						for _, item := range tools {
							definition := item.Definition()
							if original, ok := languageOriginalDescriptions[definition.Name]; ok {
								definition.Description = original
							}
							if variant == "clarified" && languageDescriptionCandidates[definition.Name] != "" {
								definition.Description = languageOriginalDescriptions[definition.Name] + languageDescriptionCandidates[definition.Name]
							}
							definitions = append(definitions, definition)
						}
						messages := []provider.Message{{Role: "system", Content: content.Text("Use exactly one requested tool now. All tool arguments are under input. Every declared field must be present. Use null for default limit, cursor and optional values. Do not answer with prose.")}}
						if history == "receipt" {
							// A controlled protocol receipt isolates conversational grounding.
							// It is synthetic; the separate harness trial executes real gopls.
							messages = append(messages,
								provider.Message{Role: "user", Content: content.Text("Get the outline of fixture.go.")},
								provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "outline_fixture", Name: "lsp_outline", Arguments: json.RawMessage(`{"input":{"path":"fixture.go","depth":1,"limit":null,"cursor":null}}`)}}},
								provider.Message{Role: "tool", ToolCallID: "outline_fixture", Content: content.Text(`{"items":[{"name":"Alpha","kind":"function","path":"fixture.go","selection":{"start":{"line":2,"column":6},"end":{"line":2,"column":11}},"ref":"loc_example_1"}],"truncated":false,"metadata":{"freshness":"synchronized","partial":false}}`)},
							)
						}
						messages = append(messages, provider.Message{Role: "user", Content: content.Text(tc.prompt)})
						ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
						defer cancel()
						start := time.Now()
						response, err := client.Submit(ctx, provider.Request{Tools: definitions, Messages: messages}, nil)
						m := languageDescriptionMeasurement{Context: history, Variant: variant, Expected: tc.name, Trial: trial, Calls: response.ToolCalls, LatencyMS: time.Since(start).Milliseconds(), Usage: response.Usage, ReasoningBytes: len(response.Reasoning)}
						if err != nil {
							m.Error = err.Error()
						}
						if len(response.ToolCalls) == 1 {
							call := response.ToolCalls[0]
							m.Selected = call.Name == tc.name
							if selected := byName[call.Name]; selected != nil {
								m.ValidArguments = ValidateArguments(selected, call.Arguments) == nil
							}
							var args struct {
								Input struct {
									TargetKind         string `json:"target_kind"`
									Ref                string `json:"ref"`
									Relation           string `json:"relation"`
									IncludeDeclaration bool   `json:"include_declaration"`
								} `json:"input"`
							}
							_ = json.Unmarshal(call.Arguments, &args)
							m.CorrectIntent = m.Selected && m.ValidArguments && args.Input.TargetKind == "reference" && args.Input.Ref == "loc_example_1" && ((tc.name == "lsp_navigate" && args.Input.Relation == "definition") || (tc.name == "lsp_references" && args.Input.IncludeDeclaration))
						}
						measurements = append(measurements, m)
						raw, _ := json.Marshal(m)
						t.Log(string(raw))
						if !m.CorrectIntent || m.Error != "" {
							t.Error("incorrect call; retained in comparison measurements")
						}
					})
				}
			}
		}
	}
}
