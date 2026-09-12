package harness_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type telemetryScript struct {
	mu                 sync.Mutex
	steps              map[string]int
	submits, counts    atomic.Int32
	finished, measured chan struct{}
}

func (p *telemetryScript) Submit(_ context.Context, r provider.Request) (provider.Response, error) {
	p.submits.Add(1)
	p.mu.Lock()
	p.steps[string(r.Agent)]++
	n := p.steps[string(r.Agent)]
	p.mu.Unlock()
	if n == 1 {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "ping", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	close(p.finished)
	return provider.Response{Content: "done"}, nil
}
func (p *telemetryScript) CountTokens(context.Context, provider.Request) (int64, error) {
	p.counts.Add(1)
	close(p.measured)
	return 42, nil
}

type pingTool struct{}

func (pingTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "ping", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (pingTool) Call(context.Context, tool.Call) (tool.Result, error) { return tool.Text("pong"), nil }
func TestAutomaticTelemetryDoesNotDependOnObserver(t *testing.T) {
	for _, attached := range []bool{false, true} {
		p := &telemetryScript{steps: map[string]int{}, finished: make(chan struct{}), measured: make(chan struct{})}
		cfg := harness.DefaultConfig()
		cfg.Web = nil
		cfg.LocalTools = false
		s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p, Root: harness.AgentDependencies{Tools: []tool.Tool{pingTool{}}}})
		if err != nil {
			t.Fatal(err)
		}
		var detached chan struct{}
		if attached {
			sub := s.Subscribe(0)
			detached = make(chan struct{})
			go func() {
				defer close(detached)
				defer sub.Close()
				for {
					if _, err := sub.Next(context.Background()); err != nil {
						return
					}
				}
			}()
		}
		if _, err = s.Send(s.Root(), "run"); err != nil {
			t.Fatal(err)
		}
		for _, done := range []chan struct{}{p.finished, p.measured} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("headless work or telemetry stalled")
			}
		}
		if err = s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if attached {
			<-detached
		}
		if p.submits.Load() != 2 || p.counts.Load() != 1 {
			t.Fatal(attached, p.submits.Load(), p.counts.Load())
		}
		inspection := s.Inspect()
		if !inspection.Coverage.ToolDiagnostics || inspection.Coverage.ModelRequests || !inspection.Config.Root.InjectedProvider || inspection.Config.Root.Model != nil {
			t.Fatal(inspection)
		}
		if err = s.Dispose(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
func TestEffectiveModelConfigResolvesPresetWithoutAliasing(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.Model.BaseURL = "http://localhost:9999/v1"
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	info := s.Configuration()
	m := info.Root.Model
	if m == nil || m.Preset != "qwen3.6-coding" || m.Generation.MaxTokens == nil || *m.Generation.MaxTokens != 32768 || m.BaseURL != "http://localhost:9999/v1" {
		t.Fatal(m)
	}
	*m.Generation.MaxTokens = 1
	info.Root.Tools[0].Parameters[0] = '!'
	again := s.Configuration()
	if *again.Root.Model.Generation.MaxTokens != 32768 || again.Root.Tools[0].Parameters[0] == '!' {
		t.Fatal("configuration aliases reader")
	}
}

type concurrentTelemetry struct {
	mu           sync.Mutex
	steps        map[string]int
	active, peak atomic.Int32
	started      chan struct{}
	release      chan struct{}
}

func (p *concurrentTelemetry) Submit(_ context.Context, r provider.Request) (provider.Response, error) {
	p.mu.Lock()
	p.steps[string(r.Agent)]++
	n := p.steps[string(r.Agent)]
	p.mu.Unlock()
	if n == 1 {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "1", Name: "ping", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	return provider.Response{Content: "done"}, nil
}
func (p *concurrentTelemetry) CountTokens(ctx context.Context, _ provider.Request) (int64, error) {
	n := p.active.Add(1)
	defer p.active.Add(-1)
	for old := p.peak.Load(); n > old; old = p.peak.Load() {
		if p.peak.CompareAndSwap(old, n) {
			break
		}
	}
	p.started <- struct{}{}
	select {
	case <-p.release:
		return 42, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func TestAutomaticTelemetryBoundsConcurrentProviderIO(t *testing.T) {
	p := &concurrentTelemetry{steps: map[string]int{}, started: make(chan struct{}, 6), release: make(chan struct{})}
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.Telemetry.Concurrency = 2
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	for i := 0; i < 6; i++ {
		created, err := s.CreateAgent(s.Root(), agent.Spec{Provider: p, Tools: []tool.Tool{pingTool{}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Send(created.AgentID, "run"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-p.started:
		case <-time.After(time.Second):
			t.Fatal("missing count")
		}
	}
	if p.active.Load() != 2 {
		t.Fatal(p.active.Load())
	}
	close(p.release)
	for i := 0; i < 4; i++ {
		select {
		case <-p.started:
		case <-time.After(time.Second):
			t.Fatal("queued count missing")
		}
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.peak.Load() != 2 {
		t.Fatal(p.peak.Load())
	}
}
func TestAutomaticTelemetryCanBeDisabled(t *testing.T) {
	p := &telemetryScript{steps: map[string]int{}, finished: make(chan struct{}), measured: make(chan struct{})}
	cfg := harness.DefaultConfig()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.Telemetry.ContextTokens = false
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p, Root: harness.AgentDependencies{Tools: []tool.Tool{pingTool{}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err = s.Send(s.Root(), "run"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.finished:
	case <-time.After(time.Second):
		t.Fatal("script stalled")
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.counts.Load() != 0 {
		t.Fatal("disabled telemetry made requests")
	}
}
