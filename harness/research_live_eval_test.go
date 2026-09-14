package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type limitedEvalProvider struct {
	inner provider.Provider
	calls atomic.Int32
}

func (p *limitedEvalProvider) Submit(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) > 24 {
		return provider.Response{}, errors.New("live evaluation exceeded 24 model calls")
	}
	return p.inner.Submit(ctx, r, o)
}

// Opt-in, two bounded sequential trials. Fixture facts are evaluation inputs,
// never additions to the production prompts. No browsing or external repository.
func TestResearchLivePlanComparison(t *testing.T) {
	url, model := os.Getenv("STRAP_RESEARCH_EVAL_URL"), os.Getenv("STRAP_RESEARCH_EVAL_MODEL")
	if url == "" || model == "" {
		t.Skip("set STRAP_RESEARCH_EVAL_URL and STRAP_RESEARCH_EVAL_MODEL")
	}
	output := os.Getenv("STRAP_RESEARCH_EVAL_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"direct", "research"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			dir := t.TempDir()
			fixture := map[string]string{
				"PLAN.md":       "# Quota validation\nRequirements: reject negative byte counts; accept counts from 0 through 10 inclusive; reject counts greater than 10. Verify each boundary with tests. Report implemented behavior, gaps and uncertainty; do not change source files.\n",
				"go.mod":        "module example.com/quota\n\ngo 1.23\n",
				"quota.go":      "package quota\n\nfunc Allows(bytes int) bool { return bytes <= 10 }\n",
				"quota_test.go": "package quota\nimport \"testing\"\nfunc TestUpperBoundary(t *testing.T) { if !Allows(10) || Allows(11) { t.Fatal(\"upper boundary\") } }\n",
			}
			for name, body := range fixture {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			maxTokens := 8192
			thinking := true
			temperature := 1.0
			topP := 0.95
			cfg := harness.DefaultConfig()
			cfg.Dir = dir
			cfg.Web = nil
			cfg.Telemetry.ContextTokens = false
			cfg.Model = harness.ModelConfig{Backend: "vllm", Preset: "none", BaseURL: url, Model: model, Timeout: 2 * time.Minute, Generation: vllm.Generation{MaxTokens: &maxTokens, EnableThinking: &thinking, Temperature: &temperature, TopP: &topP}}
			base, err := cfg.Model.NewProvider(nil)
			if err != nil {
				t.Fatal(err)
			}
			p := &limitedEvalProvider{inner: base}
			cfg.Events.JSONLPath = filepath.Join(output, mode+"-"+time.Now().UTC().Format("20060102T150405.000000000")+".jsonl")
			s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: p})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Dispose(context.Background())
			sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			request := "Compare PLAN.md with quota.go and quota_test.go. Report concrete requirement gaps and what the tests do and do not establish. Run the existing local test if useful. Do not change files. "
			if mode == "research" {
				request += "Use one researcher for the investigation, then synthesize its delivered brief."
			} else {
				request += "Inspect directly without delegating."
			}
			start := time.Now()
			if _, err = s.Send(s.Root(), request); err != nil {
				t.Fatal(err)
			}
			rootCalls, rejected := 0, 0
			delivered := false
			answer := ""
			for answer == "" {
				record, err := sub.Next(ctx)
				if err != nil {
					t.Fatalf("incomplete trial: root_calls=%d total_calls=%d rejected=%d elapsed=%s trace=%s: %v", rootCalls, p.calls.Load(), rejected, time.Since(start), cfg.Events.JSONLPath, err)
				}
				if record.Kind == "output_started" && record.Agent == string(s.Root()) {
					rootCalls++
				}
				if record.Kind != "tool" && record.Kind != "message" && record.Kind != "work" {
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
					if e.Activity.Err != nil {
						rejected++
					}
				case conversation.WorkEvent:
					if e.Event.Kind == "research_delivered" {
						delivered = true
					}
				case conversation.MessageEvent:
					if e.Message.From == s.Root() && e.Message.To == message.User && e.Message.Kind == message.Reply {
						answer = e.Message.Content
					}
				}
			}
			metrics := map[string]any{"mode": mode, "model": model, "elapsed_ms": time.Since(start).Milliseconds(), "root_calls": rootCalls, "total_calls": p.calls.Load(), "rejected_tools": rejected, "research_delivered": delivered, "answer": answer, "trace": cfg.Events.JSONLPath}
			data, _ := json.MarshalIndent(metrics, "", "  ")
			if err = os.WriteFile(cfg.Events.JSONLPath+".metrics.json", data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Log(string(data))
			if mode == "research" && !delivered {
				t.Error("final answer preceded research delivery")
			}
			lower := strings.ToLower(answer)
			if !strings.Contains(lower, "negative") || !strings.Contains(lower, "test") {
				t.Error("summary omitted expected gap or test limitation")
			}
			for name, body := range fixture {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != body {
					t.Errorf("fixture changed: %s", name)
				}
			}
			if err = s.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
