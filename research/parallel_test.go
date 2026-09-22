package research

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
)

type aliasWeb struct{ fetches atomic.Int32 }

func (w *aliasWeb) Search(_ context.Context, query string) ([]Hit, error) {
	if query == "failed scout" {
		return nil, errors.New("search unavailable")
	}
	return []Hit{{URL: "https://example.test/" + query}}, nil
}
func (w *aliasWeb) Fetch(ctx context.Context, url string) (Page, error) {
	w.fetches.Add(1)
	return (fixtureWeb{}).Fetch(ctx, url)
}

func TestParallelScoutFailureKeepsEvidenceAndDeduplicatesRedirects(t *testing.T) {
	p := modelFunc(func(ctx context.Context, q provider.Request, o provider.Observer) (provider.Response, error) {
		if strings.Contains(q.Messages[0].Content.Text(), "Stage: plan\n") {
			return response(plan{Questions: []question{{Question: "first", Query: "alias-one"}, {Question: "second", Query: "alias-two"}, {Question: "failure", Query: "failed scout"}}}), nil
		}
		return fixtureModel(ctx, q, o)
	})
	cfg := DefaultConfig()
	cfg.Standard.Scouts = 3
	cfg.ModelConcurrency = 3
	e, err := New(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	web := &aliasWeb{}
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := e.Run(ctx, testBinding(), testRequest(), Dependencies{Web: web, Record: func(_ context.Context, e Event) error { events = append(events, e); return nil }})
	if err != nil || report.Status != "partial" || len(report.Findings) != 1 || len(report.Sources) != 1 || web.fetches.Load() != 2 || report.Spend.FetchSuccesses != 2 {
		t.Fatalf("%+v %v", report, err)
	}
	sources := 0
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatal("non-monotonic retained progress")
		}
		if event.Kind == "source" {
			sources++
		}
	}
	if sources != 1 {
		t.Fatal("duplicate retained source records", sources)
	}
}
