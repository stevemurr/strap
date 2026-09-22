package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
)

type modelFunc func(context.Context, provider.Request, provider.Observer) (provider.Response, error)

func (f modelFunc) Submit(ctx context.Context, q provider.Request, o provider.Observer) (provider.Response, error) {
	return f(ctx, q, o)
}

type fixtureWeb struct{}

func (fixtureWeb) Search(context.Context, string) ([]Hit, error) {
	return []Hit{{Title: "Primary evidence", URL: "https://example.test/research"}}, nil
}
func (fixtureWeb) Fetch(context.Context, string) (Page, error) {
	return Page{FinalURL: "https://example.test/research", Title: "Evidence", ContentType: "text/html", Text: "The measured latency was 12 ms. This result applies to the measured sample."}, nil
}
func testRequest() Request {
	return Request{WorkID: "work-test", Question: "What was measured?", SuccessCriteria: []string{"State measured latency"}}
}
func testBinding() Binding {
	return Binding{WorkID: "work-test", Assignment: 1, Actor: "researcher", InvocationID: "researcher/tool-1"}
}
func response(v any) provider.Response {
	b, _ := json.Marshal(v)
	in, out := int64(100), int64(30)
	return provider.Response{Content: string(b), Usage: &provider.Usage{InputTokens: &in, OutputTokens: &out}}
}
func fixtureModel(ctx context.Context, q provider.Request, _ provider.Observer) (provider.Response, error) {
	system := q.Messages[0].Content.Text()
	var input map[string]json.RawMessage
	_ = json.Unmarshal([]byte(q.Messages[1].Content.Text()), &input)
	switch {
	case strings.Contains(system, "Stage: plan\n"):
		return response(plan{Questions: []question{{Question: "Latency", Query: "latency"}}}), nil
	case strings.Contains(system, "Stage: scout\n"):
		var observation struct {
			Hits   []Hit  `json:"hits"`
			Source Source `json:"source"`
		}
		_ = json.Unmarshal(input["observation"], &observation)
		if observation.Source.ID == "" {
			return response(scoutAction{Action: "read", URL: observation.Hits[0].URL}), nil
		}
		return response(scoutAction{Action: "finish", Status: "complete", Findings: []candidate{{Claim: "The measured latency was 12 ms.", Basis: "observed", Evidence: []candidateCitation{{SourceID: observation.Source.ID, Quote: "The measured latency was 12 ms."}}}}}), nil
	case strings.Contains(system, "Stage: reconcile\n"):
		return response(plan{}), nil
	case strings.Contains(system, "Stage: synthesize\n"):
		var fs []Finding
		_ = json.Unmarshal(input["findings"], &fs)
		return response(draft{FindingIDs: []string{fs[0].ID}, SummaryIDs: []string{fs[0].ID}}), nil
	case strings.Contains(system, "Stage: verify\n"):
		var fs []Finding
		_ = json.Unmarshal(input["claims"], &fs)
		v := verification{}
		for _, f := range fs {
			v.Verdicts = append(v.Verdicts, verdict{ID: f.ID, Verdict: "supported", Reason: "Exact source measurement"})
		}
		return response(v), nil
	case strings.Contains(system, "Stage: coverage\n"):
		var fs []Finding
		_ = json.Unmarshal(input["accepted_findings"], &fs)
		return response(coverageResult{Coverage: []Coverage{{Index: 0, Requirement: "State measured latency", Status: "met", FindingIDs: []string{fs[0].ID}, Reason: "Measurement reported"}}}), nil
	}
	return provider.Response{}, fmt.Errorf("unexpected stage %s", system)
}
func TestEngineRetainsEvidenceAndVerifiesReport(t *testing.T) {
	e, err := New(Config{}, modelFunc(fixtureModel))
	if err != nil {
		t.Fatal(err)
	}
	var records []Event
	p, err := e.Run(context.Background(), testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(_ context.Context, e Event) error { records = append(records, e); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "complete" || len(p.Findings) != 1 || p.Findings[0].Verdict != "supported" || p.Spend.ModelCalls != 7 || p.Spend.InputTokens != 700 || p.Spend.Fetches != 1 {
		t.Fatalf("unexpected report: %+v", p)
	}
	var source Source
	for i, e := range records {
		if e.Sequence != uint64(i+1) {
			t.Fatal("record sequence")
		}
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
		if e.Source != nil {
			source = *e.Source
		}
	}
	c := p.Findings[0].Evidence[0]
	if source.Text[c.Start:c.End] != c.Quote || p.Sources[0].Text != "" || source.SHA256 != hash(source.Text) {
		t.Fatal("source provenance lost")
	}
	if len(Digest(p)) > 12<<10 || strings.Contains(string(Digest(p)), source.Text) {
		t.Fatal("digest leaked source body")
	}
	if records[len(records)-1].Kind != "finished" {
		t.Fatal("missing finish")
	}
}
func TestCancellationRetainsSourceAndSettlesWithoutGeneration(t *testing.T) {
	e, _ := New(Config{}, modelFunc(fixtureModel))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var records []Event
	p, err := e.Run(ctx, testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(ctx context.Context, e Event) error {
		if ctx.Err() != nil {
			t.Fatal("settlement inherited cancellation")
		}
		records = append(records, e)
		if e.Kind == "source" {
			cancel()
		}
		return nil
	}})
	if err != nil || p.Status != "partial" || p.StopReason != "cancelled" || len(p.Sources) != 1 || len(p.Findings) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
	if p.Spend.ModelCalls != 2 || records[len(records)-1].Kind != "finished" {
		t.Fatal("generated after cancellation or lost finish")
	}
}
func TestCaptureFailureRemainsError(t *testing.T) {
	e, _ := New(Config{}, modelFunc(fixtureModel))
	fail := errors.New("disk full")
	_, err := e.Run(context.Background(), testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(_ context.Context, e Event) error {
		if e.Kind == "source" {
			return fail
		}
		return nil
	}})
	if !errors.Is(err, fail) {
		t.Fatalf("capture failure hidden: %v", err)
	}
}
func TestUnsupportedClaimCannotBecomeVerifiedInference(t *testing.T) {
	p := modelFunc(func(ctx context.Context, q provider.Request, o provider.Observer) (provider.Response, error) {
		if strings.Contains(q.Messages[0].Content.Text(), "Stage: coverage\n") {
			return response(coverageResult{Coverage: []Coverage{{Index: 0, Requirement: "State measured latency", Status: "unmet", Reason: "No supported claim"}}}), nil
		}
		result, err := fixtureModel(ctx, q, o)
		if strings.Contains(q.Messages[0].Content.Text(), "Stage: verify\n") {
			var v verification
			_ = json.Unmarshal([]byte(result.Content), &v)
			for i := range v.Verdicts {
				v.Verdicts[i].Verdict = "contradicted"
			}
			return response(v), nil
		}
		if strings.Contains(q.Messages[0].Content.Text(), "Stage: coverage\n") {
			return response(coverageResult{Coverage: []Coverage{{Index: 0, Requirement: "State measured latency", Status: "unmet", Reason: "No supported claim"}}}), nil
		}
		return result, err
	})
	e, _ := New(Config{}, p)
	r, err := e.Run(context.Background(), testBinding(), testRequest(), Dependencies{Web: fixtureWeb{}, Record: func(context.Context, Event) error { return nil }})
	if err != nil || r.Status != "partial" || len(r.Findings) != 0 || len(r.Rejected) != 1 || strings.Contains(r.Summary, "12 ms") {
		t.Fatalf("unsupported conclusion leaked: %+v %v", r, err)
	}
}
func TestQuietProviderDeadlineAndBusyAdmission(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	p := modelFunc(func(ctx context.Context, _ provider.Request, _ provider.Observer) (provider.Response, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	})
	cfg := DefaultConfig()
	cfg.Standard.Duration = 80 * time.Millisecond
	e, _ := New(cfg, p)
	deps := Dependencies{Web: fixtureWeb{}, Record: func(context.Context, Event) error { return nil }}
	done := make(chan Report, 1)
	go func() { p, _ := e.Run(context.Background(), testBinding(), testRequest(), deps); done <- p }()
	<-entered
	if _, err := e.Run(context.Background(), testBinding(), testRequest(), deps); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.StopReason != "deadline" {
			t.Fatal(r.StopReason)
		}
	case <-time.After(time.Second):
		t.Fatal("quiet model did not cancel")
	}
}
func TestCitationsRejectFabricationAndAmbiguousSpans(t *testing.T) {
	r := &run{id: "r", sources: map[string]Source{"s": {ID: "s", Text: "same same", SHA256: hash("same same")}}}
	for _, c := range []candidate{{Claim: "x", Basis: "observed", Evidence: []candidateCitation{{SourceID: "invented", Quote: "same"}}}, {Claim: "x", Basis: "observed", Evidence: []candidateCitation{{SourceID: "s", Quote: "same"}}}, {Claim: "x", Basis: "inferred", Evidence: []candidateCitation{{SourceID: "s", Quote: "same same"}}}} {
		if _, err := r.finding(c); err == nil {
			t.Fatal("accepted invalid evidence")
		}
	}
}
func TestDomainPolicyAndInputValidation(t *testing.T) {
	p, err := newPolicy([]string{"Example.Test"}, []string{"bad.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]bool{"https://example.test": true, "https://docs.example.test": true, "https://bad.example.test": false, "https://fakeexample.test": false} {
		if p.permits(url) != want {
			t.Fatal(url)
		}
	}
	if _, err := newPolicy([]string{"https://example.test"}, nil); err == nil {
		t.Fatal("scheme accepted as domain")
	}
	r := testRequest()
	r.SuccessCriteria = nil
	if r.Validate() == nil {
		t.Fatal("empty criteria accepted")
	}
	r = testRequest()
	n := int64(1000)
	r.MaxTokens = &n
	e, _ := New(Config{}, modelFunc(fixtureModel))
	if e.Validate(r) == nil {
		t.Fatal("unsupported strict token budget accepted")
	}
}
