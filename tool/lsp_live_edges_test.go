package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

// First-call tests use actual gopls outputs and execute the model's selection.
// They assert the returned location/facts, not merely JSON validity. The normal
// harness evaluation separately measures behavior with competing local tools.
func TestLiveLanguageEdges(t *testing.T) {
	base, model := os.Getenv("STRAP_LIVE_BASE_URL"), os.Getenv("STRAP_LIVE_MODEL")
	if os.Getenv("STRAP_LIVE_LSP_EDGES") != "1" || base == "" || model == "" {
		t.Skip("set STRAP_LIVE_LSP_EDGES=1, STRAP_LIVE_BASE_URL and STRAP_LIVE_MODEL; requires gopls")
	}
	dir := t.TempDir()
	fixture := map[string]string{
		"go.mod":                "module example.com/edges\n\ngo 1.24.0\n",
		"pricing/rate.go":       "package pricing\n\n// RateFor returns a customer price.\nfunc RateFor(customer string) int { if customer == \"vip\" { return 8 }; return 12 }\n",
		"pricing/pair.go":       "package pricing\n\nfunc Pair() int { return RateFor(\"vip\") + RateFor(\"retail\") }\n",
		"pricing/local.go":      "package pricing\n\nfunc Local() int { RateFor := func(string) int { return 42 }; return RateFor(\"vip\") }\n",
		"legacy/rate.go":        "package legacy\n\nfunc RateFor(customer string) int { return 99 }\n",
		"invoice/total.go":      "package invoice\n\nimport \"example.com/edges/pricing\"\n\nfunc Total(units int, customer string) int {\n\treturn units * pricing.RateFor(customer)\n}\n",
		"preview/emoji file.go": "package preview\n\nimport \"example.com/edges/pricing\"\n\nfunc Emoji() int { _ = \"🌎\"; return pricing.RateFor(\"vip\") }\n",
		"broken/broken.go":      "package broken\n\nfunc Broken() int { return \"bad\" }\n",
		"stale/stale.go":        "package stale\n\nfunc Original() int { return 1 }\n",
	}
	for name, text := range fixture {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := lsp.GoConfig()
	config.Dir = dir
	config.DiagnosticTimeoutMS = 5000
	m, err := lsp.New(config, lsp.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	tools, err := LSPTools(m)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Tool{}
	var definitions []provider.ToolDefinition
	for _, item := range tools {
		byName[item.Definition().Name] = item
		definitions = append(definitions, item.Definition())
	}
	maxTokens, temperature, topP, topK, penalty, thinking := 8192, 1.0, 0.95, 20, 1.1, true
	generation := vllm.Generation{MaxTokens: &maxTokens, Temperature: &temperature, TopP: &topP, TopK: &topK, RepetitionPenalty: &penalty, EnableThinking: &thinking}
	client, err := vllm.New(vllm.Config{BaseURL: base, Model: model, Generation: generation})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	symbols, err := m.Symbols(ctx, lsp.SymbolQuery{Query: "RateFor"})
	if err != nil {
		t.Fatal(err)
	}
	var pricingRef string
	for _, item := range symbols.Items {
		if item.Path == "pricing/rate.go" {
			pricingRef = item.Ref
		}
	}
	if pricingRef == "" {
		t.Fatal("fixture symbol unavailable")
	}
	page, err := m.Symbols(ctx, lsp.SymbolQuery{Query: "RateFor", PageQuery: lsp.PageQuery{Limit: 1}})
	if err != nil || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	old, err := m.Outline(ctx, lsp.OutlineQuery{Path: "stale/stale.go"})
	if err != nil {
		t.Fatal(err)
	}
	// Invalidate a genuinely issued handle before the stale-handle scenario.
	if err := os.WriteFile(filepath.Join(dir, "stale/stale.go"), []byte("package stale\n\n// shifted\nfunc Original() int { return 2 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.Changed("stale/stale.go")
	receipt := func(name, args string, value any) []provider.Message {
		raw, _ := json.Marshal(value)
		return []provider.Message{
			{Role: "user", Content: content.Text("Retrieve the relevant code information.")},
			{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "fixture_receipt", Name: name, Arguments: json.RawMessage(args)}}},
			{Role: "tool", ToolCallID: "fixture_receipt", Content: content.Text(string(raw))},
		}
	}
	type edge struct {
		name, prompt string
		history      []provider.Message
		names        []string
		path         string
		line, column int
		ref          string
		diagnostic   bool
	}
	pairLine := strings.Split(fixture["pricing/pair.go"], "\n")[2]
	pairColumn := utf8.RuneCountInString(pairLine[:strings.LastIndex(pairLine, "RateFor")]) + 1
	localLine := strings.Split(fixture["pricing/local.go"], "\n")[2]
	localColumn := utf8.RuneCountInString(localLine[:strings.Index(localLine, "RateFor")]) + 1
	cases := []edge{
		{name: "tabbed_call", prompt: "Find the definition of the RateFor call on line 6 of invoice/total.go. The exact source line is:\n\treturn units * pricing.RateFor(customer)", names: []string{"lsp_navigate"}, path: "pricing/rate.go", line: 4},
		{name: "unicode_and_spaced_path", prompt: "Find the definition of RateFor called on line 5 of preview/emoji file.go. The exact source line is:\nfunc Emoji() int { _ = \"🌎\"; return pricing.RateFor(\"vip\") }", names: []string{"lsp_navigate"}, path: "pricing/rate.go", line: 4},
		{name: "quoted_spaced_path", prompt: "Find the definition of RateFor called on line 5 of `preview/emoji file.go`. The exact source line is:\nfunc Emoji() int { _ = \"🌎\"; return pricing.RateFor(\"vip\") }", names: []string{"lsp_navigate"}, path: "pricing/rate.go", line: 4},
		{name: "repeated_identifier", prompt: "Inspect the RateFor call with argument \"retail\" on line 3 of pricing/pair.go, including its source. The exact source line is:\n" + pairLine, names: []string{"lsp_inspect"}, path: "pricing/pair.go", line: 3, column: pairColumn},
		{name: "local_shadow", prompt: "Find the definition of the RateFor call with argument \"vip\" on line 3 of pricing/local.go. The exact source line is:\n" + localLine, names: []string{"lsp_navigate"}, path: "pricing/local.go", line: 3, column: localColumn},
		{name: "duplicate_declarations", prompt: "Find usages of pricing.RateFor using its returned handle. Exclude its declaration and the unrelated legacy.RateFor.", history: receipt("lsp_symbols", `{"input":{"query":"RateFor","path":null,"limit":null,"cursor":null}}`, symbols), names: []string{"lsp_references"}, path: "invoice/total.go", ref: pricingRef},
		{name: "pagination", prompt: "Continue the previous symbol search with its next_cursor. Preserve its original arguments, including the page size of 1.", history: receipt("lsp_symbols", `{"input":{"query":"RateFor","path":null,"limit":1,"cursor":null}}`, page), names: []string{"lsp_symbols"}},
		{name: "callsite_filter", prompt: "Find the declaration of pricing.RateFor called on line 6 of invoice/total.go: return units * pricing.RateFor(customer). Its declaration is in another file, so filtering declarations to invoice/total.go cannot find it.", names: []string{"lsp_navigate", "lsp_symbols"}, path: "pricing/rate.go", line: 4},
		{name: "empty_diagnostic_cache", prompt: "An earlier cached diagnostics view had no entries and freshness unknown. Check whether broken/broken.go currently has errors; do not infer it is clean from the empty cache.", names: []string{"lsp_diagnostics"}, path: "broken/broken.go", diagnostic: true},
		{name: "stale_handle", prompt: "stale/stale.go has been edited since this outline: Original moved down one line and its body changed. Rediscover its current location before inspecting; the old reference is invalid.", history: receipt("lsp_outline", `{"input":{"path":"stale/stale.go","depth":1,"limit":null,"cursor":null}}`, old), names: []string{"lsp_outline", "lsp_symbols"}, path: "stale/stale.go", line: 4},
	}
	var measurements []map[string]any
	output := os.Getenv("STRAP_LIVE_LSP_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		raw, _ := json.MarshalIndent(map[string]any{"model": model, "generation": generation, "tools": definitions, "measurements": measurements}, "", "  ")
		path := filepath.Join(output, "edges-"+time.Now().UTC().Format("20060102T150405.000000000")+".json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Error(err)
		}
		t.Log("metrics:", path)
	}()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			messages := []provider.Message{{Role: "system", Content: content.Text("Use exactly one appropriate tool as your next step. All arguments are under input; every declared field must be present and nullable fields may use null. Do not answer with prose before using the tool.")}}
			messages = append(messages, tc.history...)
			messages = append(messages, provider.Message{Role: "user", Content: content.Text(tc.prompt)})
			start := time.Now()
			response, err := client.Submit(ctx, provider.Request{Tools: definitions, Messages: messages}, nil)
			measurement := map[string]any{"case": tc.name, "latency_ms": time.Since(start).Milliseconds(), "usage": response.Usage, "calls": response.ToolCalls, "passed": false}
			defer func() {
				measurements = append(measurements, measurement)
				raw, _ := json.Marshal(measurement)
				t.Log(string(raw))
			}()
			fail := func(format string, args ...any) {
				measurement["error"] = fmt.Sprintf(format, args...)
				t.Fatalf(format, args...)
			}
			if err != nil {
				fail("model: %v", err)
			}
			if len(response.ToolCalls) != 1 {
				fail("expected one first call, got %d", len(response.ToolCalls))
			}
			call := response.ToolCalls[0]
			allowed := false
			for _, name := range tc.names {
				allowed = allowed || call.Name == name
			}
			if !allowed {
				fail("unnecessary or incorrect first tool: %s", call.Name)
			}
			selected := byName[call.Name]
			if err := ValidateArguments(selected, call.Arguments); err != nil {
				fail("invalid arguments: %v", err)
			}
			var args struct {
				Input struct {
					Ref    string   `json:"ref"`
					Cursor *string  `json:"cursor"`
					Paths  []string `json:"paths"`
				} `json:"input"`
			}
			_ = json.Unmarshal(call.Arguments, &args)
			if tc.ref != "" && args.Input.Ref != tc.ref {
				fail("did not reuse the requested symbol's actual handle")
			}
			if tc.name == "pagination" && (args.Input.Cursor == nil || *args.Input.Cursor != page.NextCursor) {
				fail("did not continue the issued page cursor")
			}
			if tc.diagnostic && (len(args.Input.Paths) != 1 || args.Input.Paths[0] != tc.path) {
				fail("did not request explicit-file diagnostics")
			}
			result, err := selected.Call(ctx, Call{Arguments: call.Arguments})
			measurement["result"] = result.Content.Text()
			if err != nil {
				fail("tool rejected: %v", err)
			}
			var items []lsp.Item
			if call.Name == "lsp_inspect" {
				var out lsp.Inspection
				if err := json.Unmarshal([]byte(result.Content.Text()), &out); err != nil {
					fail("decode: %v", err)
				}
				items = []lsp.Item{out.Location}
			} else {
				var out lsp.Page
				if err := json.Unmarshal([]byte(result.Content.Text()), &out); err != nil {
					fail("decode: %v", err)
				}
				items = out.Items
			}
			if len(items) == 0 {
				fail("empty result does not establish the requested target")
			}
			found := tc.path == ""
			for _, item := range items {
				if item.Path != tc.path {
					continue
				}
				if tc.line > 0 && (item.Selection == nil || item.Selection.Start.Line != tc.line) {
					continue
				}
				if tc.column > 0 && (item.Selection == nil || item.Selection.Start.Column != tc.column) {
					continue
				}
				if tc.diagnostic && !strings.Contains(item.Message, "int") {
					continue
				}
				found = true
			}
			if !found {
				fail("returned the wrong source location or diagnostic")
			}
			measurement["passed"] = true
		})
	}
}
