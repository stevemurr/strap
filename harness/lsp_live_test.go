package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/internal/modelcatalog"
	"github.com/stevemurr/strap/lsp"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/tool"
)

type languageLiveCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Result    string          `json:"result"`
	Error     string          `json:"error,omitempty"`
}

type languageLiveMetrics struct {
	Model                string             `json:"model"`
	Profile              string             `json:"profile"`
	ElapsedMS            int64              `json:"elapsed_ms"`
	ModelCalls           int32              `json:"model_calls"`
	InputTokens          int64              `json:"input_tokens"`
	OutputTokens         int64              `json:"output_tokens"`
	RejectedCalls        int                `json:"rejected_calls"`
	InvalidArguments     int                `json:"invalid_arguments"`
	ReferenceReuses      int                `json:"reference_reuses"`
	UnknownReferences    int                `json:"unknown_references"`
	RequiredFactsPresent bool               `json:"required_facts_present"`
	Unexercised          []string           `json:"unexercised_tools"`
	Tools                map[string]int     `json:"tools"`
	Calls                []languageLiveCall `json:"calls"`
	Answer               string             `json:"answer"`
	Trace                string             `json:"trace"`
	Failures             []string           `json:"failures"`
	Generation           vllm.Generation    `json:"generation"`
}

// This opt-in evaluation uses the normal harness prompts/tool roster and a real
// gopls process. The model sees a task, not a prescribed tool sequence. Temporary
// fixture code is the only project content sent to the endpoint. Outcomes and
// rejected calls remain in the metrics even if the model later recovers.
func TestLiveLanguageTraversal(t *testing.T) {
	base, model := os.Getenv("STRAP_LIVE_BASE_URL"), os.Getenv("STRAP_LIVE_MODEL")
	if os.Getenv("STRAP_LIVE_LSP") != "1" || base == "" || model == "" {
		t.Skip("set STRAP_LIVE_LSP=1, STRAP_LIVE_BASE_URL and STRAP_LIVE_MODEL")
	}
	profile := os.Getenv("STRAP_LIVE_PROFILE")
	if profile == "" {
		profile = "qwen3.6"
	}
	trials := 1
	if raw := os.Getenv("STRAP_LIVE_LSP_TRIALS"); raw != "" {
		var err error
		trials, err = strconv.Atoi(raw)
		if err != nil || trials < 1 || trials > 10 {
			t.Fatal("STRAP_LIVE_LSP_TRIALS must be 1..10")
		}
	}
	output := os.Getenv("STRAP_LIVE_LSP_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	for trial := 1; trial <= trials; trial++ {
		t.Run(fmt.Sprintf("trial_%d", trial), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			dir := t.TempDir()
			fixture := map[string]string{
				"go.mod":           "module example.com/lspfixture\n\ngo 1.24.0\n",
				"pricing/rate.go":  "package pricing\n\n// RateFor returns the unit price for the customer tier.\nfunc RateFor(customer string) int {\n\tif customer == \"vip\" { return 8 }\n\treturn 12\n}\n",
				"invoice/total.go": "package invoice\n\nimport \"example.com/lspfixture/pricing\"\n\nfunc Total(units int, customer string) int {\n\treturn units * pricing.RateFor(customer)\n}\n",
				"preview/quote.go": "package preview\n\nimport \"example.com/lspfixture/pricing\"\n\nfunc Quote() int { return pricing.RateFor(\"retail\") }\n",
				"legacy/rate.go":   "package legacy\n\nfunc RateFor(customer string) int { return 99 }\nfunc Quote() int { return RateFor(\"old\") }\n",
				"broken/broken.go": "package broken\n\nfunc Broken() int { return \"bad\" }\n",
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
			cfg := harness.DefaultConfig()
			cfg.Dir, cfg.Web = dir, nil
			cfg.Telemetry.ContextTokens = false
			var err error
			cfg.Model, _, err = modelcatalog.Resolve("../internal/modelcatalog/models.json", profile, 2*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Model.BaseURL, cfg.Model.Model = base, model
			cfg.Model.Timeout = 90 * time.Second
			// Preserve profile sampling/thinking, with bounded evaluation output.
			maxTokens := 8192
			cfg.Model.Generation.MaxTokens = &maxTokens
			inner, err := cfg.Model.NewProvider(nil)
			if err != nil {
				t.Fatal(err)
			}
			p := &limitedEvalProvider{inner: inner}
			prefix := filepath.Join(output, fmt.Sprintf("traversal-%s-%d-%s", profile, trial, time.Now().UTC().Format("20060102T150405.000000000")))
			cfg.Events.JSONLPath = prefix + ".jsonl"
			s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: p})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if err := s.Dispose(closeCtx); err != nil {
					t.Error(err)
				}
			}()
			sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			metrics := languageLiveMetrics{Model: model, Profile: profile, Trace: cfg.Events.JSONLPath, Tools: map[string]int{}, Generation: cfg.Model.Generation}
			start := time.Now()
			defer func() {
				metrics.ElapsedMS, metrics.ModelCalls = time.Since(start).Milliseconds(), p.calls.Load()
				raw, _ := json.MarshalIndent(metrics, "", "  ")
				if err := os.WriteFile(prefix+".metrics.json", raw, 0600); err != nil {
					t.Error(err)
				}
				t.Logf("model_calls=%d tools=%v invalid_arguments=%d rejected=%d reused_refs=%d unknown_refs=%d elapsed=%s metrics=%s", metrics.ModelCalls, metrics.Tools, metrics.InvalidArguments, metrics.RejectedCalls, metrics.ReferenceReuses, metrics.UnknownReferences, time.Since(start), prefix+".metrics.json")
			}()
			// Definitions alone are sufficient to independently validate emitted
			// arguments; actual execution belongs to the harness's shared manager.
			validationTools, err := tool.LSPTools(new(lsp.Manager))
			if err != nil {
				t.Fatal(err)
			}
			byName := map[string]tool.Tool{}
			for _, item := range validationTools {
				byName[item.Definition().Name] = item
			}
			refs := map[string]bool{}
			seenCallers := map[string]bool{}
			diagnosticObserved := false
			request := "Investigate this Go project directly without delegating or modifying files. Follow the pricing.RateFor call inside invoice.Total to its definition, inspect its implementation, and identify every caller of that exact function. Exclude unrelated functions with the same name. Explain the vip and regular unit prices, citing the declaration and caller files/functions. Also check language diagnostics for broken/broken.go and report its error."
			if _, err = s.Send(s.Root(), request); err != nil {
				t.Fatal(err)
			}
			for metrics.Answer == "" {
				record, err := sub.Next(ctx)
				if err != nil {
					metrics.Failures = append(metrics.Failures, "incomplete trial: "+err.Error())
					t.Fatal(metrics.Failures)
				}
				if record.Kind != "tool" && record.Kind != "message" && record.Kind != "usage" {
					continue
				}
				resolved, err := s.ResolveRecord(ctx, record)
				if err != nil {
					t.Fatal(err)
				}
				fact, err := eventcodec.DecodeEvent(resolved)
				if err != nil {
					t.Fatal(err)
				}
				switch e := fact.(type) {
				case conversation.ToolEvent:
					a := e.Activity
					if a.FinishedAt.IsZero() {
						continue
					}
					call := languageLiveCall{Name: a.Call.Name, Arguments: a.Call.Arguments, Result: a.Result.Content.Text()}
					metrics.Tools[call.Name]++
					if a.Err != nil {
						metrics.RejectedCalls++
						call.Error = a.Err.Error()
					}
					metrics.Calls = append(metrics.Calls, call)
					t.Logf("tool=%s arguments=%s error=%s", call.Name, call.Arguments, call.Error)
					if target := byName[call.Name]; target != nil {
						if err := tool.ValidateArguments(target, call.Arguments); err != nil {
							metrics.InvalidArguments++
						}
						var args struct {
							Input struct {
								Ref string `json:"ref"`
							} `json:"input"`
						}
						_ = json.Unmarshal(call.Arguments, &args)
						if args.Input.Ref != "" {
							if refs[args.Input.Ref] {
								metrics.ReferenceReuses++
							} else {
								metrics.UnknownReferences++
							}
						}
						var result any
						_ = json.NewDecoder(strings.NewReader(call.Result)).Decode(&result)
						collectLanguageRefs(result, refs)
						var page lsp.Page
						_ = json.NewDecoder(strings.NewReader(call.Result)).Decode(&page)
						for _, item := range page.Items {
							if call.Name == "lsp_references" && call.Error == "" {
								seenCallers[item.Path] = true
							}
							if call.Name == "lsp_diagnostics" && item.Path == "broken/broken.go" && strings.Contains(item.Message, "int") {
								diagnosticObserved = true
							}
						}
					}
				case conversation.UsageEvent:
					if u := e.Observation.Usage; u != nil {
						if u.InputTokens != nil {
							metrics.InputTokens += *u.InputTokens
						}
						if u.OutputTokens != nil {
							metrics.OutputTokens += *u.OutputTokens
						}
					}
				case conversation.MessageEvent:
					if e.Message.From == s.Root() && e.Message.To == message.User && e.Message.Kind == message.Reply {
						metrics.Answer = e.Message.Content
					}
				}
			}
			check := func(ok bool, why string) {
				if !ok {
					metrics.Failures = append(metrics.Failures, why)
					t.Error(why)
				}
			}
			check(metrics.InvalidArguments == 0 && metrics.RejectedCalls == 0, "tool arguments or execution were rejected")
			check(metrics.UnknownReferences == 0, "used a handle that was never returned")
			for _, name := range []string{"lsp_inspect", "lsp_navigate", "lsp_references", "lsp_diagnostics"} {
				if metrics.Tools[name] == 0 {
					metrics.Unexercised = append(metrics.Unexercised, name)
				}
			}
			// Choosing read_file instead of inspect/navigation can still answer
			// the task correctly. Record missing tool coverage separately.
			taskFailureStart := len(metrics.Failures)
			check(seenCallers["invoice/total.go"] && seenCallers["preview/quote.go"] && !seenCallers["legacy/rate.go"], "reference results did not distinguish the target's callers from the unrelated same-name function")
			check(diagnosticObserved, "expected type diagnostic was not observed")
			for _, fact := range []string{"pricing/rate.go", "invoice/total.go", "preview/quote.go", "broken/broken.go", "8", "12"} {
				check(strings.Contains(metrics.Answer, fact), "answer omitted expected fact: "+fact)
			}
			for name, before := range fixture {
				after, err := os.ReadFile(filepath.Join(dir, name))
				check(err == nil && string(after) == before, "fixture modified: "+name)
			}
			// This checks required fixture facts, not every extra claim in prose.
			// Full answer correctness still requires manual review of the trace.
			metrics.RequiredFactsPresent = len(metrics.Failures) == taskFailureStart
		})
	}
}

func collectLanguageRefs(value any, refs map[string]bool) {
	switch v := value.(type) {
	case map[string]any:
		if ref, ok := v["ref"].(string); ok && ref != "" {
			refs[ref] = true
		}
		for _, child := range v {
			collectLanguageRefs(child, refs)
		}
	case []any:
		for _, child := range v {
			collectLanguageRefs(child, refs)
		}
	}
}
