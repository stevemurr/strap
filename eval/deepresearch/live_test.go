package deepresearch

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/research"
)

type draftRecorder struct {
	provider.Provider
	mu     sync.Mutex
	claims []research.Claim
}

func (p *draftRecorder) Submit(ctx context.Context, q provider.Request, o provider.Observer) (provider.Response, error) {
	if len(q.Messages) > 1 && strings.Contains(q.Messages[0].Content.Text(), "Stage: verify\n") {
		var input struct {
			Claims []research.Claim `json:"claims"`
		}
		if json.Unmarshal([]byte(q.Messages[1].Content.Text()), &input) == nil {
			p.mu.Lock()
			for _, claim := range input.Claims {
				found := false
				for _, old := range p.claims {
					if old.ID == claim.ID {
						found = true
					}
				}
				if !found {
					p.claims = append(p.claims, claim)
				}
			}
			p.mu.Unlock()
		}
	}
	return p.Provider.Submit(ctx, q, o)
}

// This test never uses live search: only the explicitly configured model endpoint
// receives requests. It writes all evidence and outcomes, including failures.
func TestLiveFrozenCorpus(t *testing.T) {
	endpoint, model := os.Getenv("STRAP_DEEP_RESEARCH_EVAL_URL"), os.Getenv("STRAP_DEEP_RESEARCH_EVAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("set STRAP_DEEP_RESEARCH_EVAL_URL and STRAP_DEEP_RESEARCH_EVAL_MODEL")
	}
	output := os.Getenv("STRAP_DEEP_RESEARCH_EVAL_OUTPUT")
	if output == "" {
		t.Fatal("set STRAP_DEEP_RESEARCH_EVAL_OUTPUT to retain evaluation artifacts")
	}
	repetitions := 3
	if value := os.Getenv("STRAP_DEEP_RESEARCH_EVAL_REPETITIONS"); value != "" {
		var err error
		repetitions, err = strconv.Atoi(value)
		if err != nil || repetitions < 1 || repetitions > 3 {
			t.Fatal("repetitions must be 1..3")
		}
	}
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	filter := os.Getenv("STRAP_DEEP_RESEARCH_EVAL_CASE")
	matched := false
	for _, fixture := range fixtures {
		if filter != "" && filter != fixture.ID {
			continue
		}
		matched = true
		for _, scouts := range []int{1, 2} {
			for repetition := 1; repetition <= repetitions; repetition++ {
				t.Run(fixture.ID+"/scouts-"+strconv.Itoa(scouts)+"/trial-"+strconv.Itoa(repetition), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					defer cancel()
					transport := http.DefaultTransport.(*http.Transport).Clone()
					defer transport.CloseIdleConnections()
					cap := 8192
					cfg := harness.ModelConfig{Backend: "vllm", BaseURL: endpoint, Model: model, Timeout: time.Minute, Generation: vllm.Generation{MaxTokens: &cap}}
					base, err := cfg.NewProvider(&http.Client{Transport: transport, Timeout: cfg.Timeout})
					if err != nil {
						t.Fatal(err)
					}
					model := &draftRecorder{Provider: base}
					limits := research.DefaultConfig()
					limits.Standard.Duration = 2 * time.Minute
					limits.Standard.Scouts = scouts
					limits.ModelConcurrency = scouts
					engine, err := research.New(limits, model)
					if err != nil {
						t.Fatal(err)
					}
					var events []research.Event
					var sources []research.Source
					request := research.Request{WorkID: "eval", Question: fixture.Question, SuccessCriteria: fixture.Criteria, AllowDomains: fixture.AllowDomains, BlockDomains: fixture.BlockDomains}
					report, runErr := engine.Run(ctx, research.Binding{WorkID: "eval", Assignment: 1, Actor: "researcher", InvocationID: "eval/tool-1"}, request, research.Dependencies{Web: Corpus{fixture.Pages}, Record: func(_ context.Context, e research.Event) error {
						events = append(events, e)
						if e.Source != nil {
							sources = append(sources, *e.Source)
						}
						return nil
					}})
					draft := report
					draft.Claims = model.claims
					artifact := map[string]any{"fixture": fixture, "model": cfg.Model, "generation": cfg.Generation, "scouts": scouts, "repetition": repetition, "config": limits, "report": report, "events": events, "score": Evaluate(report, sources, len(fixture.Criteria), Labels{}), "pre_verification_findings": model.claims, "pre_verification_score": Evaluate(draft, sources, len(fixture.Criteria), Labels{})}
					if runErr != nil {
						artifact["error"] = runErr.Error()
					}
					path := filepath.Join(output, fixture.ID+"-scouts"+strconv.Itoa(scouts)+"-trial"+strconv.Itoa(repetition)+"-"+time.Now().UTC().Format("20060102T150405.000000000")+".json")
					if err := os.MkdirAll(output, 0700); err != nil {
						t.Fatal(err)
					}
					raw, err := json.MarshalIndent(artifact, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
					t.Logf("%s: %s/%s, %d model calls; %s", fixture.ID, report.Status, report.StopReason, report.Spend.ModelCalls, path)
					if runErr != nil {
						t.Error(runErr)
					}
					if report.Status == "failed" {
						t.Errorf("no evidence retained: %s", report.StopReason)
					}
				})
			}
		}
	}
	if !matched {
		t.Fatal("unknown evaluation case")
	}
}
