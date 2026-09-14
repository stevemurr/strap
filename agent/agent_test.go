package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type modelFunc func(context.Context, provider.Request) (provider.Response, error)

func (f modelFunc) Submit(c context.Context, r provider.Request, observer provider.Observer) (provider.Response, error) {
	return f(c, r)
}

type senderFunc func(context.Context, message.Draft) (message.Receipt, error)

func (f senderFunc) Send(c context.Context, d message.Draft) (message.Receipt, error) { return f(c, d) }

type customTool struct {
	name string
	err  error
}

func (t customTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: t.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (t customTool) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Text("result"), t.err
}

type invalidTool struct{ customTool }

func (invalidTool) Validate() error { return errors.New("bad definition") }
func config() agent.Config {
	return agent.Config{ID: "worker", ReplyTo: message.User, Inbox: inbox.New[message.Message](), Outbox: senderFunc(func(context.Context, message.Draft) (message.Receipt, error) { return message.Receipt{}, nil }), Spec: agent.Spec{Provider: modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		return provider.Response{Content: "answer"}, nil
	})}}
}
func mustAgent(t *testing.T, c agent.Config) *agent.Agent {
	t.Helper()
	a, err := agent.New(c)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}
func awaitState(t *testing.T, ch <-chan agent.State, want agent.State) {
	t.Helper()
	for {
		if await(t, ch) == want {
			return
		}
	}
}

func TestAgentConfigurationValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*agent.Config)
		want   string
	}{
		{"provider", func(c *agent.Config) { c.Spec.Provider = nil }, "requires"},
		{"inbox", func(c *agent.Config) { c.Inbox = nil }, "requires"},
		{"outbox", func(c *agent.Config) { c.Outbox = nil }, "requires"},
		{"nil tool", func(c *agent.Config) { c.Spec.Tools = []tool.Tool{nil} }, "nil tool"},
		{"invalid tool", func(c *agent.Config) { c.Spec.Tools = []tool.Tool{invalidTool{}} }, "invalid tool"},
		{"unnamed", func(c *agent.Config) { c.Spec.Tools = []tool.Tool{customTool{}} }, "no name"},
		{"duplicate", func(c *agent.Config) { c.Spec.Tools = []tool.Tool{customTool{name: "x"}, customTool{name: "x"}} }, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config()
			tc.change(&c)
			if _, err := agent.New(c); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestToolBatchSettlesBeforeSteeringAndPreservesReplyTarget(t *testing.T) {
	c := config()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	receipts := make(chan message.Receipt, 8)
	activities := make(chan agent.ToolActivity, 8)
	batches := make(chan agent.ToolBatch, 1)
	drafts := make(chan message.Draft, 1)
	c.OnConsumed = func(r message.Receipt) { receipts <- r }
	c.OnTool = func(a agent.ToolActivity) { activities <- a }
	c.OnToolBatch = func(b agent.ToolBatch) { batches <- b }
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		drafts <- d
		cancel()
		return message.Receipt{}, nil
	})
	c.Spec.Tools = []tool.Tool{customTool{name: "read"}, customTool{name: "broken", err: errors.New("disk unavailable")}}
	calls := 0
	c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
		calls++
		if calls == 1 {
			if len(r.Messages) != 3 || r.Messages[1].Envelope.Kind != message.Observation || r.Messages[2].Envelope.ID != "input" || len(r.Tools) != 2 {
				t.Errorf("initial request: %+v", r)
			}
			c.Inbox.Send(message.Message{ID: "steer", Kind: message.Instruction, Content: "steer"})
			c.Inbox.Send(message.Message{ID: "notification", Kind: message.Notification, Content: "notice"})
			return provider.Response{ToolCalls: []provider.ToolCall{{ID: "one", Name: "read", Arguments: json.RawMessage(`{}`)}, {ID: "two", Name: "broken", Arguments: json.RawMessage(`{}`)}, {ID: "three", Name: "missing", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		if len(r.Messages) != 9 || r.Messages[4].Content.Text() != "result" || r.Messages[5].Content.Text() != "result\nTool error: disk unavailable" || r.Messages[6].Content.Text() != "Tool error: unknown tool: missing" || r.Messages[7].Envelope.ID != "steer" {
			t.Errorf("batch history: %+v", r.Messages)
		}
		return provider.Response{Content: "answer"}, nil
	})
	a := mustAgent(t, c)
	c.Inbox.Send(message.Message{ID: "observe", Kind: message.Observation, Content: "observation"})
	c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "go"})
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	if d := await(t, drafts); d.ReplyTo != "steer" || d.Content != "answer" || d.To != message.User {
		t.Fatal(d)
	}
	if batch := await(t, batches); batch.ContextRevision != 7 || len(batch.Calls) != 3 {
		t.Fatal(batch)
	}
	for i := 0; i < 6; i++ {
		event := await(t, activities)
		if event.StartedAt.IsZero() || (i%2 == 0) != event.FinishedAt.IsZero() {
			t.Fatal(event)
		}
		if i == 3 && event.Err == nil {
			t.Fatal("missing failed tool event")
		}
	}
	for _, id := range []message.MessageID{"observe", "input", "steer", "notification"} {
		r := await(t, receipts)
		if r.MessageID != id || r.Status != message.Consumed {
			t.Fatal(r)
		}
	}
	if a.State() != agent.Stopped || !a.State().Terminal() {
		t.Fatal(a.State())
	}
	if err := a.Run(ctx); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatal(err)
	}
}
func TestAgentFailuresAreTerminal(t *testing.T) {
	for _, kind := range []string{"provider", "empty response", "outbox", "closed inbox", "stop during submit", "stop after consume", "stop before tool"} {
		t.Run(kind, func(t *testing.T) {
			c := config()
			c.Inbox.Send(message.Message{Kind: message.Instruction, Content: "go"})
			var a *agent.Agent
			switch kind {
			case "provider":
				c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
					return provider.Response{}, errors.New("provider failed")
				})
			case "empty response":
				c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) { return provider.Response{}, nil })
			case "outbox":
				c.Outbox = senderFunc(func(context.Context, message.Draft) (message.Receipt, error) {
					return message.Receipt{}, errors.New("route failed")
				})
			case "closed inbox":
				c.Inbox.Drain()
				c.Inbox.Close()
			case "stop during submit":
				c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
					a.RequestStop()
					return provider.Response{Content: "discard"}, nil
				})
			case "stop after consume":
				c.OnConsumed = func(message.Receipt) { a.RequestStop() }
			case "stop before tool":
				c.Spec.Tools = []tool.Tool{customTool{name: "read"}}
				c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
					return provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "read"}, {ID: "2", Name: "read"}}}, nil
				})
				c.OnTool = func(e agent.ToolActivity) {
					if !e.FinishedAt.IsZero() {
						a.RequestStop()
					}
				}
			}
			a = mustAgent(t, c)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := a.Run(ctx); err == nil {
				t.Fatal("expected failure")
			}
			if !a.State().Terminal() {
				t.Fatal(a.State())
			}
			if _, err := a.Pause(); err == nil {
				t.Fatal("paused terminal agent")
			}
			if _, err := a.Resume(); err == nil {
				t.Fatal("resumed terminal agent")
			}
			if s := a.RequestStop(); !s.Terminal() {
				t.Fatal(s)
			}
		})
	}
}
func TestIdlePauseResumeAndStop(t *testing.T) {
	c := config()
	states := make(chan agent.State, 32)
	c.OnState = func(s agent.State) { states <- s }
	called := make(chan struct{}, 1)
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		called <- struct{}{}
		return provider.Response{Content: "ok"}, nil
	})
	a := mustAgent(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	if a.State() != agent.Idle {
		t.Fatal("new agent is not idle", a.State())
	}
	if _, err := a.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Pause(); err != nil {
		t.Fatal(err)
	}
	awaitState(t, states, agent.Paused)
	if s, err := a.Pause(); err != nil || s != agent.Paused {
		t.Fatal(s, err)
	}
	c.Inbox.Send(message.Message{Kind: message.Instruction, Content: "go"})
	if _, err := a.Resume(); err != nil {
		t.Fatal(err)
	}
	await(t, called)
	awaitState(t, states, agent.Idle)
	a.RequestStop()
	if _, err := a.Pause(); err == nil {
		t.Fatal("pause while stopping")
	}
	if _, err := a.Resume(); err == nil {
		t.Fatal("resume while stopping")
	}
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestPausedAgentCanBeCanceled(t *testing.T) {
	c := config()
	states := make(chan agent.State, 16)
	c.OnState = func(s agent.State) { states <- s }
	a := mustAgent(t, c)
	a.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	awaitState(t, states, agent.Paused)
	cancel()
	if err := await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type counterModel struct {
	modelFunc
	requests []provider.Request
}

func (p *counterModel) CountTokens(_ context.Context, r provider.Request) (int64, error) {
	p.requests = append(p.requests, r)
	return 42, nil
}
func TestCountTokensUsesHistoricalSnapshotAndTools(t *testing.T) {
	c := config()
	plain := mustAgent(t, c)
	if _, err := plain.CountTokens(context.Background(), 1); err == nil {
		t.Fatal("unsupported counter")
	}
	p := &counterModel{modelFunc: c.Spec.Provider.(modelFunc)}
	c.Spec.Provider = p
	c.Spec.Tools = []tool.Tool{customTool{name: "read"}}
	a := mustAgent(t, c)
	for _, rev := range []uint64{0, 2} {
		if _, err := a.CountTokens(context.Background(), rev); err == nil {
			t.Fatal("accepted revision", rev)
		}
	}
	for i := 0; i < 2; i++ {
		n, err := a.CountTokens(context.Background(), 1)
		if err != nil || n != 42 {
			t.Fatal(n, err)
		}
		r := p.requests[i]
		if len(r.Messages) != 1 || len(r.Tools) != 1 || r.Tools[0].Name != "read" || string(r.Tools[0].Parameters) != `{"type":"object"}` {
			t.Fatal(r)
		}
		r.Tools[0].Parameters[0] = '!'
	}
}

func TestUnassignedAgentRemainsIdleWithoutRunningTransition(t *testing.T) {
	c := config()
	states := make(chan agent.State, 8)
	started := make(chan struct{}, 1)
	c.OnState = func(s agent.State) { states <- s }
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		if _, ok := e.(agent.HistoryAppended); ok {
			started <- struct{}{}
		}
		return nil
	})
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		t.Error("unassigned agent called provider")
		return provider.Response{}, nil
	})
	a := mustAgent(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); await(t, done) }()
	await(t, started)
	select {
	case s := <-states:
		t.Fatal("unassigned agent emitted lifecycle transition", s)
	case <-time.After(20 * time.Millisecond):
	}
	if a.State() != agent.Idle {
		t.Fatal(a.State())
	}
}
