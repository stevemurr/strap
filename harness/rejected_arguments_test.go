package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/work"
)

type rejectedArgumentsTransport struct{ body string }

func (r rejectedArgumentsTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(r.body))}, nil
}

func TestRejectedArgumentsSurviveFramedJSONLArchive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Exceed the publisher's frame size and include whitespace, escapes and
	// Unicode. The missing brace must not invalidate the surrounding trace JSON.
	raw := " \t{\"message\":\"" + strings.Repeat("hé\\n", 20000) + "\"\n"
	args, _ := json.Marshal(raw)
	body := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"bad-call","type":"function","function":{"name":"send_message","arguments":` + string(args) + `}}]}}]}`
	p, err := vllm.New(vllm.Config{BaseURL: "http://model.test", Model: "test", HTTPClient: &http.Client{Transport: rejectedArgumentsTransport{body}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, false)
	cfg.Telemetry.ContextTokens = false
	cfg.Events.JSONLPath = filepath.Join(t.TempDir(), "rejected.jsonl")
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
	if _, err := s.Send(s.Root(), "hello"); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == "tool" || e.Kind == "tool_batch" {
			t.Fatalf("rejected call was dispatched: %+v", e)
		}
		if e.Kind == "history_appended" {
			resolved, err := s.ResolveRecord(ctx, e)
			if err != nil {
				t.Fatal(err)
			}
			fact, err := eventcodec.DecodeEvent(resolved)
			if err != nil {
				t.Fatal(err)
			}
			if fact.(conversation.AgentEvent).Event.(agent.HistoryAppended).Message.Role == "assistant" {
				t.Fatal("rejected call entered model history")
			}
		}
		if e.Kind == "agent_exited" {
			break
		}
	}
	if err := s.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	trace, err := os.ReadFile(cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
		var e eventlog.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Kind != "output_finished" {
			continue
		}
		var framed record.Framed
		if err := json.Unmarshal(e.Payload, &framed); err != nil || framed.Content == nil {
			t.Fatalf("test did not exercise framing: %s, %v", e.Payload, err)
		}
		resolved, err := reader.ResolveRecord(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		fact, err := eventcodec.DecodeEvent(resolved)
		if err != nil {
			t.Fatal(err)
		}
		failure := fact.(conversation.AgentEvent).Event.(agent.OutputFinished)
		var detail *provider.ToolArgumentsError
		if failure.Status != agent.OutputFailed || failure.HistoryPosition != nil || !errors.As(failure.Err, &detail) || detail.Arguments != raw || detail.CallID != "bad-call" || detail.Name != "send_message" {
			t.Fatalf("failure evidence lost: status=%s detail=%+v error=%v", failure.Status, detail, failure.Err)
		}
		return
	}
	t.Fatal("no failed output recorded")
}

// A rejected call's retry notice must not poison projection replay: every
// later read of the session, and the wake context of every agent, depends on
// it. Before the fix the notice carried the failed output's id, the projector
// rejected it with "history output mismatch", and the root died on its next
// workflow call.
func TestRejectedCallNoticeKeepsProjectionReadable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := `{"choices":[{"finish_reason":"length","message":{"role":"assistant","tool_calls":[{"id":"cut","type":"function","function":{"name":"create_plan","arguments":"{\"steps\":[{\"title\":\"Imp"}}]}}]}`
	p, err := vllm.New(vllm.Config{BaseURL: "http://model.test", Model: "test", HTTPClient: &http.Client{Transport: rejectedArgumentsTransport{body}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, false)
	cfg.Telemetry.ContextTokens = false
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err := s.Send(s.Root(), "plan it"); err != nil {
		t.Fatal(err)
	}
	awaitAgentState(t, s, s.Root(), agent.Failed) // Two retries, then the agent gives up.
	if _, err := s.ListWork(ctx, s.Root(), work.ListQuery{Limit: 10}); err != nil {
		t.Fatalf("projection unreadable after a rejected call: %v", err)
	}
	if _, err := s.InspectAgentContext(ctx, s.Root(), conversation.InspectOptions{}); err != nil {
		t.Fatalf("agent context unreadable after a rejected call: %v", err)
	}
}
