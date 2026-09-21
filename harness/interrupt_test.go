package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type interruptModel func(context.Context, provider.Request, provider.Observer) (provider.Response, error)

func (f interruptModel) Submit(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	return f(ctx, r, o)
}

func interruptSession(t *testing.T, deps harness.Dependencies) *harness.Session {
	t.Helper()
	cfg := harness.DefaultConfig()
	cfg.LocalTools, cfg.Web, cfg.Telemetry.ContextTokens = false, nil, false
	s, err := harness.New(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Dispose(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

// await receives one value or fails the test after a generous bound.
func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting:", what)
		var zero T
		return zero
	}
}

func interruptNow(t *testing.T, s *harness.Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptCancelsDelegationAndQueuedMessagesThenContinues(t *testing.T) {
	started := make(chan struct{}, 4)
	requests := make(chan provider.Request, 8)
	var calls atomic.Int32
	p := interruptModel(func(ctx context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
		requests <- r
		if calls.Add(1) <= 3 {
			if err := o.OnDelta(provider.Delta{Text: "unfinished text"}); err != nil {
				return provider.Response{}, err
			}
			started <- struct{}{}
			<-ctx.Done()
			return provider.Response{}, ctx.Err()
		}
		return provider.Response{Content: "fresh answer"}, nil
	})
	s := interruptSession(t, harness.Dependencies{Provider: p})
	root := s.Root()
	worker := createWorker(t, s, roster.Implementor)
	researcher := createWorker(t, s, roster.Researcher)
	if _, err := s.Send(root, "original task"); err != nil {
		t.Fatal(err)
	}
	implementation, err := s.AssignWork(context.Background(), root, work.AssignmentRequest{Kind: work.Implementation, Assignee: worker, Task: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	research, err := s.AssignWork(context.Background(), root, work.AssignmentRequest{Kind: work.Research, Assignee: researcher, Task: "investigate"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		await(t, started, "interruption test")
		await(t, requests, "interruption test")
	}
	queued, err := s.Send(root, "queued instruction must not run")
	if err != nil {
		t.Fatal(err)
	}
	interruptNow(t, s)
	if s.State() != harness.Open || s.Root() != root {
		t.Fatal("interruption replaced or closed the session")
	}
	for _, a := range s.Agents() {
		if a.State != agent.Interrupted {
			t.Fatalf("agent not held: %+v", a)
		}
		output, err := s.InspectOutput(context.Background(), identity.OutputID{Agent: a.ID, Call: 1})
		if err != nil || output.Output.Status != agent.OutputCanceled || output.Output.TextBytes != uint64(len("unfinished text")) {
			t.Fatal("canceled output lost its observed prefix", output, err)
		}
	}
	for _, id := range []work.ID{implementation.ID, research.ID} {
		w, err := s.GetWork(context.Background(), root, id)
		if err != nil || w.State != work.Cancelled {
			t.Fatalf("work survived interruption: %+v %v", w, err)
		}
	}
	if receipt, _ := s.Receipt(queued.MessageID); receipt.Status != message.Undelivered {
		t.Fatal(receipt)
	}
	if _, err := s.ResumeAgent(root); err == nil {
		t.Fatal("resume continued interrupted execution")
	}
	if _, err := s.CreateAgent(context.Background(), root, roster.CreateRequest{Role: roster.Implementor}); !errors.Is(err, harness.ErrInterrupted) {
		t.Fatal("created agent while held", err)
	}
	before, err := s.InspectAgent(root, conversation.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	interruptNow(t, s)
	after, err := s.InspectAgent(root, conversation.InspectOptions{})
	if err != nil || after.ContextRevision != before.ContextRevision {
		t.Fatal("repeated interrupt changed history", err)
	}
	if _, err := s.Send(root, "new instruction"); err != nil {
		t.Fatal(err)
	}
	r := await(t, requests, "interruption test")
	var old, fresh bool
	for _, m := range r.Messages {
		old = old || strings.Contains(m.Content.Text(), "original task")
		fresh = fresh || strings.Contains(m.Content.Text(), "new instruction")
		if strings.Contains(m.Content.Text(), "queued instruction must not run") {
			t.Fatal("discarded input entered history")
		}
		if m.Role == "assistant" && strings.Contains(m.Content.Text(), "unfinished text") {
			t.Fatal("partial output entered model history")
		}
	}
	if !old || !fresh {
		t.Fatal("conversation history not retained")
	}
	awaitAgentState(t, s, root, agent.Idle)
	select {
	case <-requests:
		t.Fatal("old delegated work restarted")
	case <-time.After(25 * time.Millisecond):
	}
	// Reuse the same worker with a fresh assignment after new input.
	if _, err := s.AssignWork(context.Background(), root, work.AssignmentRequest{Kind: work.Implementation, Assignee: worker, Task: "fresh implementation"}); err != nil {
		t.Fatal(err)
	}
	await(t, requests, "interruption test")
}

func TestInterruptConcurrentWaitersRetainHistoryAndCanContinueAfterTimeout(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	requests := make(chan provider.Request, 1)
	p := interruptModel(func(ctx context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			// A dependency can report success despite cancellation. The harness
			// must neither commit this response nor execute its proposed tool.
			return provider.Response{Content: "late success", ToolCalls: []provider.ToolCall{{ID: "late", Name: "shell", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
		}
		requests <- r
		return provider.Response{Content: "ready"}, nil
	})
	s := interruptSession(t, harness.Dependencies{Provider: p})
	if _, err := s.Send(s.Root(), "start"); err != nil {
		t.Fatal(err)
	}
	await(t, started, "interruption test")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Interrupt(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	await(t, canceled, "interruption test")
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			results <- s.Interrupt(ctx)
		}()
	}
	close(release)
	for i := 0; i < cap(results); i++ {
		if err := await(t, results, "interruption test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Send(s.Root(), "continue differently"); err != nil {
		t.Fatal(err)
	}
	r := await(t, requests, "interruption test")
	notices := 0
	for _, m := range r.Messages {
		if strings.Contains(m.Content.Text(), "late success") || len(m.ToolCalls) > 0 {
			t.Fatal("canceled model response was committed", m)
		}
		if strings.Contains(m.Content.Text(), "The user interrupted execution.") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatal("concurrent stop requests duplicated interruption", notices)
	}
}

type interruptFailStore struct{ eventlog.Store }

func (s interruptFailStore) Append(ctx context.Context, d eventlog.Data) (eventlog.Event, error) {
	if d.Kind == "agent_state" && strings.Contains(string(d.Payload), `"interrupted"`) {
		return eventlog.Event{}, errors.New("interruption capture failed")
	}
	return s.Store.Append(ctx, d)
}

func TestInterruptCaptureFailureDoesNotReopenAdmission(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.LocalTools, cfg.Web, cfg.Telemetry.ContextTokens = false, nil, false
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("ready"), EventStore: func(id string) (eventlog.Store, error) {
		store, err := eventlog.NewMemory(id, eventlog.Limits{})
		return interruptFailStore{store}, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Interrupt(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("capture failure was hidden or hung", err)
	}
	if _, err := s.Send(s.Root(), "unsafe continuation"); err == nil {
		t.Fatal("failed interruption reopened admission")
	}
	if s.Capture().CaptureError == "" {
		t.Fatal("capture failure was not retained")
	}
}

type interruptBlockingStore struct {
	eventlog.Store
	block            atomic.Bool
	started, release chan struct{}
}

func (s *interruptBlockingStore) Append(ctx context.Context, d eventlog.Data) (eventlog.Event, error) {
	if d.Kind == "message" && s.block.CompareAndSwap(true, false) {
		close(s.started)
		<-s.release
	}
	return s.Store.Append(ctx, d)
}

func TestInterruptRacingNewInputCannotReleaseNewerFence(t *testing.T) {
	store := &interruptBlockingStore{started: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-store.release:
		default:
			close(store.release)
		}
	}()
	requests := make(chan provider.Request, 2)
	p := &observeProvider{requests: requests}
	s := interruptSession(t, harness.Dependencies{Provider: p, EventStore: func(id string) (eventlog.Store, error) {
		var err error
		store.Store, err = eventlog.NewMemory(id, eventlog.Limits{})
		return store, err
	}})
	interruptNow(t, s)
	store.block.Store(true)
	type result struct {
		receipt message.Receipt
		err     error
	}
	sent := make(chan result, 1)
	go func() { r, err := s.Send(s.Root(), "racing new input"); sent <- result{r, err} }()
	await(t, store.started, "interruption test")
	stopping := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		stopping <- s.Interrupt(ctx)
	}()
	// Interrupt must honor its deadline even while Send is blocked publishing.
	if err := await(t, stopping, "interruption test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(store.release)
	r := await(t, sent, "interruption test")
	if r.err != nil {
		t.Fatal(r.err)
	}
	interruptNow(t, s)
	if receipt, _ := s.Receipt(r.receipt.MessageID); receipt.Status != message.Undelivered {
		t.Fatal("racing Send released the new stop fence", receipt)
	}
	select {
	case <-requests:
		t.Fatal("canceled input reached the model")
	default:
	}
	if _, err := s.Send(s.Root(), "actual next instruction"); err != nil {
		t.Fatal(err)
	}
	request := await(t, requests, "interruption test")
	for _, m := range request.Messages {
		if strings.Contains(m.Content.Text(), "racing new input") {
			t.Fatal("discarded input entered history")
		}
	}
}

type interruptTool struct {
	name string
	call func(context.Context) (tool.Result, error)
}

func (t interruptTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: t.name, Parameters: t.InputContract().Schema()}
}
func (t interruptTool) Call(ctx context.Context, _ tool.Call) (tool.Result, error) {
	return t.call(ctx)
}

func TestInterruptSettlesToolBatchWithoutRetryingEffects(t *testing.T) {
	started := make(chan struct{})
	requests := make(chan provider.Request, 2)
	var calls, writes, skipped atomic.Int32
	p := interruptModel(func(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
		if calls.Add(1) == 1 {
			return provider.Response{ToolCalls: []provider.ToolCall{
				{ID: "saved", Name: "save", Arguments: json.RawMessage(`{"input":{}}`)},
				{ID: "partial", Name: "slow", Arguments: json.RawMessage(`{"input":{}}`)},
				{ID: "skipped", Name: "later", Arguments: json.RawMessage(`{"input":{}}`)},
			}}, nil
		}
		requests <- r
		return provider.Response{Content: "continued"}, nil
	})
	s := interruptSession(t, harness.Dependencies{Provider: p, Root: harness.AgentDependencies{Tools: []tool.Tool{
		interruptTool{"save", func(context.Context) (tool.Result, error) { writes.Add(1); return tool.Text("saved effect"), nil }},
		interruptTool{"slow", func(ctx context.Context) (tool.Result, error) {
			close(started)
			<-ctx.Done()
			return tool.Text("partial effect"), ctx.Err()
		}},
		interruptTool{"later", func(context.Context) (tool.Result, error) { skipped.Add(1); return tool.Text("should not run"), nil }},
	}}})
	if _, err := s.Send(s.Root(), "do things"); err != nil {
		t.Fatal(err)
	}
	await(t, started, "interruption test")
	interruptNow(t, s)
	if writes.Load() != 1 || skipped.Load() != 0 {
		t.Fatal("effects were replayed or pending tool ran")
	}
	if _, err := s.Send(s.Root(), "different task"); err != nil {
		t.Fatal(err)
	}
	r := await(t, requests, "interruption test")
	results := map[string]string{}
	for _, m := range r.Messages {
		if m.Role == "tool" {
			if _, exists := results[m.ToolCallID]; exists {
				t.Fatal("duplicate tool result", m.ToolCallID)
			}
			results[m.ToolCallID] = m.Content.Text()
		}
	}
	if len(results) != 3 || results["saved"] != "saved effect" || !strings.Contains(results["partial"], "partial effect") || !strings.Contains(results["partial"], "context canceled") || !strings.Contains(results["skipped"], "before execution") {
		t.Fatal(results)
	}
	if writes.Load() != 1 || skipped.Load() != 0 {
		t.Fatal("tools retried on next prompt")
	}
}

func TestInterruptTimeoutRetainsFenceAndCloseJoinsCleanup(t *testing.T) {
	p := &blockedProvider{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	owned := &closer{}
	s := interruptSession(t, harness.Dependencies{Provider: p, Resources: []harness.OwnedResource{{Name: "owned", Resource: owned}}})
	defer func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	}()
	if _, err := s.Send(s.Root(), "start"); err != nil {
		t.Fatal(err)
	}
	await(t, p.started, "interruption test")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Interrupt(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	await(t, p.cancelled, "interruption test")
	if _, err := s.Send(s.Root(), "too soon"); !errors.Is(err, harness.ErrInterrupted) {
		t.Fatal("input bypassed unsettled execution", err)
	}
	if owned.calls.Load() != 0 || s.State() != harness.Open {
		t.Fatal("interruption disposed resources")
	}
	closing := make(chan error, 1)
	go func() { closing <- s.Close(context.Background()) }()
	select {
	case err := <-closing:
		t.Fatal("closed before dependency unwound", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(p.release)
	if err := await(t, closing, "interruption test"); err != nil {
		t.Fatal(err)
	}
	if owned.calls.Load() != 1 {
		t.Fatal("resource not closed exactly once")
	}
}

func TestInterruptPausedIdleSessionCanStartNewExchange(t *testing.T) {
	p := &observeProvider{requests: make(chan provider.Request, 2)}
	s := interruptSession(t, harness.Dependencies{Provider: p})
	if _, err := s.PauseAgent(s.Root()); err != nil {
		t.Fatal(err)
	}
	awaitAgentState(t, s, s.Root(), agent.Paused)
	queued, err := s.Send(s.Root(), "discard while paused")
	if err != nil {
		t.Fatal(err)
	}
	interruptNow(t, s)
	if r, _ := s.Receipt(queued.MessageID); r.Status != message.Undelivered {
		t.Fatal(r)
	}
	if _, err := s.Send(s.Root(), "fresh"); err != nil {
		t.Fatal(err)
	}
	await(t, p.requests, "interruption test")
	if _, err := s.StopAgent(s.Root()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(s.Root(), "cannot revive terminated root"); !errors.Is(err, conversation.ErrAgentStopped) {
		t.Fatal(err)
	}
}

func (t interruptTool) InputContract() tool.Contract { return emptyContract() }
