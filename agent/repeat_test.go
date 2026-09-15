package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	if err == nil || !strings.Contains(err.Error(), "identical arguments") {
		t.Fatalf("expected the repeat limit to stop the agent: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(results) != 11 {
		t.Fatalf("%d results before stopping, want 11", len(results))
	}
	for i, r := range results {
		noticed := strings.Contains(r, "consecutive call")
		if noticed != (i+1 >= 3) {
			t.Fatalf("result %d notice=%v: %q", i+1, noticed, r)
		}
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
