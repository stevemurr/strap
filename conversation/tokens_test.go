package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type countingProvider struct {
	*controlledProvider
	count func(context.Context, provider.Request) (int64, error)
}

func (p *countingProvider) CountTokens(ctx context.Context, r provider.Request) (int64, error) {
	return p.count(ctx, r)
}

func TestCountToolBatchUsesExactHistoryAndOwnsSnapshot(t *testing.T) {
	p := &countingProvider{controlledProvider: &controlledProvider{calls: make(chan call, 16)}}
	c := emptyConversation(t)
	echo := tool.Func[struct{}]{
		Spec:   tool.Definition[struct{}]{Parameters: testParameters[struct{}](t), Name: "echo"},
		Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) { return tool.Text("tool result"), nil },
	}
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: p, Tools: []tool.Tool{echo}}); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Send(c.Root(), "begin")
	p.next(t).answer <- answer{response: provider.Response{ToolCalls: []provider.ToolCall{
		{ID: "one", Name: "echo", Arguments: []byte(`{"input":{}}`)},
		{ID: "two", Name: "echo", Arguments: []byte(`{"input":{}}`)},
	}}}
	batch := event(t, c, func(e conversation.Event) bool { _, ok := e.(conversation.ToolBatchEvent); return ok }).(conversation.ToolBatchEvent)
	if batch.Agent != c.Root() || batch.Batch.ContextRevision != 5 || !reflect.DeepEqual(batch.Batch.Calls, []string{"one", "two"}) {
		t.Fatalf("incorrect completed batch: %+v", batch)
	}
	next := p.next(t)
	want := next.request
	next.text("done")
	userReply(t, c, "done")
	_, _ = c.Send(c.Root(), "newer message")
	p.next(t).text("newer answer")
	userReply(t, c, "newer answer")
	before, err := c.InspectAgent(c.Root(), conversation.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	countCalls := 0
	p.count = func(ctx context.Context, r provider.Request) (int64, error) {
		countCalls++
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !reflect.DeepEqual(r, want) {
			t.Errorf("counted wrong history: %+v, want %+v", r, want)
		}
		// No controller lock may remain held during provider I/O.
		if _, err := c.InspectAgent(c.Root(), conversation.InspectOptions{}); err != nil {
			t.Error(err)
		}
		r.Messages[0].Content[0].Text = "mutated"
		r.Messages[2].ToolCalls[0].Arguments[0] = 'x'
		r.Tools[0].Parameters[0] = 'x'
		return 12345, nil
	}
	for range 2 {
		if n, err := c.CountAgentTokens(context.Background(), c.Root(), batch.Batch.ContextRevision); err != nil || n != 12345 {
			t.Fatalf("count = %d, %v", n, err)
		}
	}
	for _, revision := range []uint64{0, 999} {
		if _, err := c.CountAgentTokens(context.Background(), c.Root(), revision); err == nil {
			t.Fatal("invalid revision accepted")
		}
	}
	if countCalls != 2 {
		t.Fatal("invalid request reached provider")
	}
	if _, err := c.CountAgentTokens(context.Background(), "unknown", 5); err == nil {
		t.Fatal("unknown agent accepted")
	}
	after, _ := c.InspectAgent(c.Root(), conversation.InspectOptions{})
	if !reflect.DeepEqual(before.Usage, after.Usage) {
		t.Fatal("tokenization changed model usage")
	}
	if _, err := c.StopAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CountAgentTokens(context.Background(), c.Root(), 5); err != nil {
		t.Fatal("stopped agent cannot be counted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.CountAgentTokens(ctx, c.Root(), 5); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func TestCountTokensUnsupportedProvider(t *testing.T) {
	c, _ := setup(t)
	if _, err := c.CountAgentTokens(context.Background(), c.Root(), 1); err == nil {
		t.Fatal("unsupported provider reported a count")
	}
}
