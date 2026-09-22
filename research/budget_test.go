package research

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
)

type budgetProvider struct{ calls atomic.Int32 }

func (p *budgetProvider) CountTokens(context.Context, provider.Request) (int64, error) {
	return 200, nil
}
func (p *budgetProvider) OutputTokenLimit() *int64 { n := int64(300); return &n }
func (p *budgetProvider) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	p.calls.Add(1)
	return provider.Response{Content: "{}"}, nil
}
func TestParallelReservationsCannotOverspendAndMissingUsageStaysCharged(t *testing.T) {
	p := &budgetProvider{}
	cfg := DefaultConfig()
	cfg.ModelConcurrency = 4
	e, _ := New(cfg, p)
	r := &run{engine: e, ctx: context.Background(), cancel: func() {}, deadline: time.Now().Add(time.Minute), binding: testBinding(), id: "research-budget", limits: Limits{ModelCalls: 100, Tokens: 2000}, deps: Dependencies{Record: func(context.Context, Event) error { return nil }}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.submit(context.Background(), "scout", nil, false)
			if err != nil && !errors.Is(err, ErrTokens) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	s := r.accounting()
	if p.calls.Load() != 3 || s.ModelCalls != 3 || s.ChargedTokens != 1500 || s.MissingInput != 3 || s.MissingOutput != 3 {
		t.Fatalf("bad reservation accounting: %+v", s)
	}
	if _, err := r.submit(context.Background(), "verify", nil, true); err != nil {
		t.Fatal("final reserve unavailable", err)
	}
	if r.accounting().ChargedTokens != 2000 {
		t.Fatal("final reserve not charged")
	}
}
func TestMalformedStageGetsOnlyOneRepair(t *testing.T) {
	var calls atomic.Int32
	p := modelFunc(func(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
		calls.Add(1)
		return provider.Response{Content: "not JSON"}, nil
	})
	e, _ := New(Config{}, p)
	r, err := e.Run(context.Background(), testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(context.Context, Event) error { return nil }})
	if err != nil || r.StopReason != "invalid_model_output" || calls.Load() != 2 {
		t.Fatalf("unexpected repair loop: %+v %v", r, err)
	}
}
func TestStreamLimitSurvivesProviderIgnoringObserverError(t *testing.T) {
	p := modelFunc(func(_ context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
		_ = o.OnDelta(provider.Delta{Text: string(make([]byte, 70<<10))})
		return provider.Response{Content: `{"questions":[]}`}, nil
	})
	e, _ := New(Config{}, p)
	r, err := e.Run(context.Background(), testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(context.Context, Event) error { return nil }})
	if err != nil || r.StopReason != "output_limit" {
		t.Fatalf("output limit lost: %+v %v", r, err)
	}
}
func TestResearchRecordQuotaPreservesTerminalSpace(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxReportBytes = 4096
	cfg.MaxSessionBytes = 2*(cfg.MaxReportBytes+8192) + 4096
	e, _ := New(cfg, modelFunc(fixtureModel))
	var final bool
	p, err := e.Run(context.Background(), testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(_ context.Context, e Event) error { final = final || e.Kind == "finished"; return nil }})
	if err != nil || !final || p.StopReason != "retention_budget" {
		t.Fatalf("no honest terminal report: %+v %v", p, err)
	}
}
func TestResearchConfigRejectsUnboundedOrImpossibleLimits(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standard.Duration = 21 * time.Minute
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("unbounded duration accepted")
	}
	cfg = DefaultConfig()
	cfg.Standard.Tokens = 1 << 62
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("overflowing token cap accepted")
	}
}
