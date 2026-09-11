package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/tool"
)

// Run explicitly against a real server; ordinary go test never uses the network:
// STRAP_LIVE_BASE_URL=http://192.168.1.237:8355 go test -race ./cmd/strap -run TestLiveTokenUsage -count=1 -v
// STRAP_LIVE_MODEL optionally overrides qwen3.6.
func TestLiveTokenUsage(t *testing.T) {
	baseURL := os.Getenv("STRAP_LIVE_BASE_URL")
	if baseURL == "" {
		t.Skip("set STRAP_LIVE_BASE_URL to test real model usage")
	}
	model := os.Getenv("STRAP_LIVE_MODEL")
	if model == "" {
		model = "qwen3.6"
	}

	t.Run("agent_tool_loop_and_followup", func(t *testing.T) {
		p, wire := liveUsageProvider(t, baseURL, model, modelOptions{backend: "vllm", overrides: vllm.Generation{MaxTokens: valuePtr(1024)}})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		c := conversation.New(ctx)
		session := workflow.New(ctx, c, agent.Spec{}, agent.Spec{})
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if err := session.Close(cleanup); err != nil {
				t.Error(err)
			}
		}()
		params, err := tool.NewParameters[struct{}]()
		if err != nil {
			t.Fatal(err)
		}
		var toolCalls atomic.Int64
		probe := tool.Func[struct{}]{
			Spec: tool.Definition[struct{}]{Name: "token_usage_probe", Description: "Return the fixed test value PROBE_OK. No side effects.", Parameters: params},
			Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) {
				toolCalls.Add(1)
				return tool.Text("PROBE_OK"), nil
			},
		}
		_, err = c.CreateAgent(message.User, agent.Spec{
			Provider: p, Prompt: prompt.Prompt{Role: "Follow the user's test instructions precisely. Keep replies short."}, Tools: []tool.Tool{probe},
		})
		if err != nil {
			t.Fatal(err)
		}
		var calls uint64
		var input, output int64
		var revision uint64
		for _, instruction := range []string{
			"Call token_usage_probe exactly once. After receiving its result, reply with PROBE_OK only. Do not call the tool again.",
			"Reply with NEXT_OK only. Do not call any tools.",
		} {
			if _, err := c.Send(c.Root(), instruction); err != nil {
				t.Fatal(err)
			}
			for replied := false; !replied; {
				e, err := session.NextEvent(ctx)
				if err != nil {
					t.Fatal(err)
				}
				switch e := e.(type) {
				case conversation.UsageEvent:
					calls++
					o := e.Observation
					if e.Agent != c.Root() || o.Call != calls || o.ContextRevision <= revision {
						t.Fatalf("incorrect usage attribution: %+v", e)
					}
					revision = o.ContextRevision
					raw := wire.at(t, int(calls)-1)
					assertLiveUsage(t, o.Usage, raw)
					input += *o.Usage.InputTokens
					output += *o.Usage.OutputTokens
					t.Logf("call=%d revision=%d input=%d output=%d finish=%s", calls, revision, *o.Usage.InputTokens, *o.Usage.OutputTokens, raw.Choices[0].FinishReason)
				case conversation.MessageEvent:
					if e.Message.To == message.User && e.Message.Kind == message.Reply {
						replied = true
					}
				case conversation.AgentExited:
					t.Fatalf("agent exited: %v", e.Err)
				}
			}
		}
		if calls != 3 || toolCalls.Load() != 1 {
			t.Fatalf("expected tool + reply + follow-up calls: calls=%d tool=%d", calls, toolCalls.Load())
		}
		inspection, err := c.InspectAgent(c.Root(), conversation.InspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		s := inspection.Usage
		if s.Calls != calls || s.InputTokens != input || s.OutputTokens != output || s.MissingInputCalls != 0 || s.MissingOutputCalls != 0 || s.Latest == nil || s.Latest.Call != calls {
			t.Fatalf("snapshot differs from raw server counts: %+v", s)
		}
		assertLiveUsage(t, s.Latest.Usage, wire.at(t, int(calls)-1))
		t.Logf("verified totals: calls=%d input=%d output=%d missing=0", calls, input, output)
	})

	for _, tc := range []struct {
		name, text string
		options    modelOptions
		truncated  bool
	}{
		{"generic_adapter", "Reply with OK only.", modelOptions{backend: "chatcompletions"}, false},
		{"truncated_completion", "Write the integers from 1 through 20 separated by spaces.", modelOptions{backend: "vllm", overrides: vllm.Generation{MaxTokens: valuePtr(1), EnableThinking: valuePtr(false)}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, wire := liveUsageProvider(t, baseURL, model, tc.options)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			r, err := p.Submit(ctx, provider.Request{Messages: []provider.Message{{Role: "user", Content: content.Text(tc.text)}}})
			if (err != nil) != tc.truncated {
				t.Fatalf("unexpected Submit error: %v", err)
			}
			raw := wire.at(t, 0)
			assertLiveUsage(t, r.Usage, raw)
			if tc.truncated && (raw.Choices[0].FinishReason != "length" || r.Content != "" || len(r.ToolCalls) != 0) {
				t.Fatalf("truncated output not rejected: response=%+v finish=%s", r, raw.Choices[0].FinishReason)
			}
			t.Logf("input=%d output=%d finish=%s rejected=%v", *r.Usage.InputTokens, *r.Usage.OutputTokens, raw.Choices[0].FinishReason, err != nil)
		})
	}
}

type liveWireCompletion struct {
	Usage struct {
		Input  *int64 `json:"prompt_tokens"`
		Output *int64 `json:"completion_tokens"`
		Total  *int64 `json:"total_tokens"`
	} `json:"usage"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type liveUsageTransport struct {
	base    http.RoundTripper
	mu      sync.Mutex
	records []liveWireCompletion
}

func (w *liveUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := w.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var record liveWireCompletion
		if err := json.Unmarshal(body, &record); err != nil {
			return nil, fmt.Errorf("capture server usage: %w", err)
		}
		w.mu.Lock()
		w.records = append(w.records, record)
		w.mu.Unlock()
	}
	return response, nil
}

func (w *liveUsageTransport) at(t *testing.T, index int) liveWireCompletion {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if index >= len(w.records) {
		t.Fatalf("missing raw response %d", index)
	}
	return w.records[index]
}

func liveUsageProvider(t *testing.T, url, model string, options modelOptions) (provider.Provider, *liveUsageTransport) {
	t.Helper()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil // The explicitly supplied endpoint is a direct local connection.
	t.Cleanup(base.CloseIdleConnections)
	wire := &liveUsageTransport{base: base}
	p, err := options.newProvider(url, model, &http.Client{Transport: wire, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return p, wire
}

func assertLiveUsage(t *testing.T, got *provider.Usage, raw liveWireCompletion) {
	t.Helper()
	if raw.Usage.Input == nil || raw.Usage.Output == nil || raw.Usage.Total == nil || len(raw.Choices) != 1 {
		t.Fatalf("server did not report complete usage: %+v", raw)
	}
	want := &provider.Usage{InputTokens: raw.Usage.Input, OutputTokens: raw.Usage.Output}
	if !reflect.DeepEqual(got, want) || *raw.Usage.Total != *raw.Usage.Input+*raw.Usage.Output || *raw.Usage.Input <= 0 || *raw.Usage.Output <= 0 {
		t.Fatalf("adapter usage %+v differs from raw server usage %+v", got, raw.Usage)
	}
}
