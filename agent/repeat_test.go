package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// A model that re-issues one rejected call verbatim gets a notice on the third
// identical call and is stopped on the twelfth, instead of running until the
// session budget ends.
func TestRepeatedIdenticalCallsAreNoticedThenStopped(t *testing.T) {
	c := config()
	c.Spec.Tools = []tool.Tool{customTool{name: "effect", err: errors.New("title is required")}}
	var mu sync.Mutex
	var results []string
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		if h, ok := e.(agent.HistoryAppended); ok && h.Message.Role == "tool" {
			mu.Lock()
			results = append(results, h.Message.Content.Text())
			mu.Unlock()
		}
		return nil
	})
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "effect", Arguments: json.RawMessage(`{"steps":[]}`)}}}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	err := mustAgent(t, c).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "same request") {
		t.Fatalf("expected the repeat limit to stop the agent: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// Even the final invocation completed and must retain its actual result.
	if len(results) != 12 {
		t.Fatalf("%d results before stopping, want 12", len(results))
	}
	for i, r := range results {
		noticed := strings.Contains(r, "consecutive call")
		if noticed != (i+1 >= 3) {
			t.Fatalf("result %d notice=%v: %q", i+1, noticed, r)
		}
	}
}

// revisionTool behaves like edit_step: every call succeeds and returns a plan
// whose revision counter has moved on, while expected_revision is bookkeeping.
type revisionTool struct{ calls *atomic.Int32 }

func (revisionTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "edit_step", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (revisionTool) BookkeepingParameters() []string { return []string{"expected_revision"} }
func (t revisionTool) Call(_ context.Context, c tool.Call) (tool.Result, error) {
	n := t.calls.Add(1)
	return tool.Text(fmt.Sprintf(`{"plan_id":"plan-x","revision":%d,"steps":[{"step_id":"s","title":"Implement"}]}`, n+1)), nil
}

// The observed shape of a no-progress loop: each edit succeeds, bumps the
// revision, and the model re-sends the same edit with the new revision.
func TestSuccessfulNoProgressLoopIsStopped(t *testing.T) {
	c := config()
	var calls atomic.Int32
	c.Spec.Tools = []tool.Tool{revisionTool{calls: &calls}}
	rev := 1
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		args, _ := json.Marshal(map[string]any{"plan_id": "plan-x", "step_id": "s", "title": "Implement", "expected_revision": rev})
		rev++
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "edit_step", Arguments: args}}}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	err := mustAgent(t, c).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "same request") || calls.Load() != 12 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
}

// outcomeTool fails with a stale-revision error until the model supplies the
// current revision, then succeeds with a fresh result each time.
type outcomeTool struct{ calls *atomic.Int32 }

func (outcomeTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "effect", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (outcomeTool) BookkeepingParameters() []string { return []string{"expected_revision"} }
func (t outcomeTool) Call(_ context.Context, c tool.Call) (tool.Result, error) {
	n := t.calls.Add(1)
	if n <= 3 {
		return tool.Result{}, errors.New("stale revision")
	}
	return tool.Text(fmt.Sprintf(`{"ok":true,"changed":"value-%d"}`, n)), nil
}

// The same request with a changing outcome is progress: repeated stale-revision
// errors count, but the success that follows restarts the count, and successes
// with different results never accumulate.
func TestChangedOutcomeResetsRepeatCount(t *testing.T) {
	c := config()
	var calls atomic.Int32
	c.Spec.Tools = []tool.Tool{outcomeTool{calls: &calls}}
	var mu sync.Mutex
	var noticed []int32
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		if h, ok := e.(agent.HistoryAppended); ok && h.Message.Role == "tool" && strings.Contains(h.Message.Content.Text(), "consecutive call") {
			mu.Lock()
			noticed = append(noticed, calls.Load())
			mu.Unlock()
		}
		return nil
	})
	replies := make(chan message.Draft, 1)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		if calls.Load() >= 10 {
			return provider.Response{Content: "done"}, nil
		}
		args, _ := json.Marshal(map[string]any{"step_id": "s", "expected_revision": calls.Load() + 1})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "effect", Arguments: args}}}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	if reply := await(t, replies); reply.Content != "done" {
		t.Fatal(reply)
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// Three identical stale-revision failures earn one notice; the seven
	// successes that follow differ in their results and earn none.
	if len(noticed) != 1 || noticed[0] != 3 {
		t.Fatalf("notices at calls %v", noticed)
	}
}

// Different arguments restart the count, so ordinary retries with corrected
// input never trip the limit.
func TestChangedArgumentsResetRepeatCount(t *testing.T) {
	c := config()
	c.Spec.Tools = []tool.Tool{customTool{name: "effect", err: errors.New("bad")}}
	calls := 0
	replies := make(chan message.Draft, 1)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		calls++
		if calls > 30 {
			return provider.Response{Content: "gave up"}, nil
		}
		args, _ := json.Marshal(map[string]int{"attempt": calls % 2})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "effect", Arguments: args}}}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	if reply := await(t, replies); reply.Content != "gave up" {
		t.Fatal(reply)
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
