package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/research"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

type deepWeb struct{}

func (deepWeb) Search(context.Context, string) ([]research.Hit, error) {
	return []research.Hit{{URL: "https://fixture.test/source", Title: "Published measurement"}}, nil
}
func (deepWeb) Fetch(context.Context, string) (research.Page, error) {
	return research.Page{FinalURL: "https://fixture.test/source", ContentType: "text/plain", Title: "Measured sample", Text: "The sample measured 12 milliseconds.\n" + strings.Repeat("Context for the measurement.\n", 600)}, nil
}

type deepModel struct {
	calls     atomic.Int32
	verifying chan struct{}
	once      sync.Once
}

func (p *deepModel) Submit(ctx context.Context, q provider.Request, _ provider.Observer) (provider.Response, error) {
	p.calls.Add(1)
	system := q.Messages[0].Content.Text()
	var input map[string]json.RawMessage
	_ = json.Unmarshal([]byte(q.Messages[1].Content.Text()), &input)
	emit := func(v any) (provider.Response, error) {
		b, _ := json.Marshal(v)
		n := int64(100)
		return provider.Response{Content: string(b), Usage: &provider.Usage{InputTokens: &n, OutputTokens: &n}}, nil
	}
	switch {
	case strings.Contains(system, "Stage: plan\n"):
		return emit(map[string]any{"questions": []any{map[string]any{"question": "Measurement", "query": "published measurement", "depends_on": []int{}}}})
	case strings.Contains(system, "Stage: scout\n"):
		var obs struct {
			Hits   []research.Hit  `json:"hits"`
			Source research.Source `json:"source"`
		}
		_ = json.Unmarshal(input["observation"], &obs)
		if obs.Source.ID == "" {
			return emit(map[string]any{"action": "read", "url": obs.Hits[0].URL})
		}
		return emit(map[string]any{"action": "finish", "status": "complete", "claims": []any{map[string]any{"claim": "The sample measured 12 milliseconds.", "basis": "observed", "limitation": "", "evidence": []any{map[string]any{"source_id": obs.Source.ID, "quote": "The sample measured 12 milliseconds."}}}}})
	case strings.Contains(system, "Stage: reconcile\n"):
		return emit(map[string]any{"questions": []any{}})
	case strings.Contains(system, "Stage: synthesize\n"):
		var fs []research.Claim
		_ = json.Unmarshal(input["claims"], &fs)
		return emit(map[string]any{"claim_ids": []string{fs[0].ID}, "summary_ids": []string{fs[0].ID}})
	case strings.Contains(system, "Stage: verify\n"):
		if p.verifying != nil {
			p.once.Do(func() { close(p.verifying) })
			<-ctx.Done()
			return provider.Response{}, ctx.Err()
		}
		var fs []research.Claim
		_ = json.Unmarshal(input["claims"], &fs)
		return emit(map[string]any{"verdicts": []any{map[string]any{"claim_id": fs[0].ID, "verdict": "supported", "reason": "Exact measurement in retained source"}}})
	case strings.Contains(system, "Stage: coverage\n"):
		var fs []research.Claim
		_ = json.Unmarshal(input["accepted_claims"], &fs)
		return emit(map[string]any{"coverage": []any{map[string]any{"index": 0, "requirement": "Report the measurement", "status": "met", "claim_ids": []string{fs[0].ID}, "reason": "Measurement is cited"}}})
	}
	return provider.Response{}, fmt.Errorf("unexpected model stage")
}

type deepWorker struct {
	calls   atomic.Int32
	receipt chan string
}

type responsiveDeepRoot struct{ called chan struct{} }

func (p responsiveDeepRoot) Submit(_ context.Context, q provider.Request, _ provider.Observer) (provider.Response, error) {
	for _, m := range q.Messages {
		if strings.Contains(m.Content.Text(), "Still available?") {
			select {
			case p.called <- struct{}{}:
			default:
			}
		}
	}
	return provider.Response{Content: "root responsive"}, nil
}

func (p *deepWorker) Submit(_ context.Context, q provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) == 1 {
		var id string
		for _, m := range q.Messages {
			if m.Envelope != nil && m.Envelope.Work != nil {
				id = string(m.Envelope.Work.ID)
			}
		}
		return operation("deep_research", research.Request{WorkID: id, Question: "What did the sample measure?", SuccessCriteria: []string{"Report the measurement"}})
	}
	if result := lastResult(q); strings.Contains(result, `"run_id":"run-`) {
		select {
		case p.receipt <- result:
		default:
		}
	}
	return operation("wait_for_input", struct{}{})
}
func deepConfig(t *testing.T) harness.Config {
	cfg := testConfig(t, false)
	cfg.DeepResearch.Enabled = true
	cfg.Telemetry.ContextTokens = false
	cfg.LSP = nil
	cfg.Events.Queue.Bytes = 8192
	return cfg
}

func TestDeepResearchDefaultsAndRetrievalAvailability(t *testing.T) {
	for _, tt := range []struct {
		name                            string
		disabled, noWeb, injected, want bool
	}{
		{name: "default", want: true},
		{name: "disabled", disabled: true},
		{name: "no retrieval", noWeb: true},
		{name: "injected retrieval", noWeb: true, injected: true, want: true},
		{name: "disabled with injected retrieval", disabled: true, noWeb: true, injected: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := harness.DefaultConfig()
			if !cfg.DeepResearch.Enabled {
				t.Fatal("deep research is not enabled by default")
			}
			cfg.Dir, cfg.LocalTools, cfg.LSP = t.TempDir(), false, nil
			cfg.Telemetry.ContextTokens = false
			if tt.disabled {
				cfg.DeepResearch.Enabled = false
			}
			if tt.noWeb {
				cfg.Web = nil
			}
			deps := harness.Dependencies{Provider: textResponse("ready")}
			if tt.injected {
				deps.ResearchWeb = deepWeb{}
			}
			s, err := harness.New(context.Background(), cfg, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Dispose(context.Background())
			effective := s.Configuration()
			if s.Config().DeepResearch.Enabled != tt.want || effective.DeepResearch.Enabled != tt.want {
				t.Fatal("effective configuration does not reflect available retrieval")
			}
			registered, reader := false, false
			for _, def := range effective.Researcher.Tools {
				registered = registered || def.Name == "deep_research"
				reader = reader || def.Name == "get_research_run"
			}
			if registered != tt.want || reader != tt.want {
				t.Fatalf("research tools registered = %v/%v, want %v", registered, reader, tt.want)
			}
			// Other roles read the delivered brief, never the researcher's runs.
			for role, tools := range map[string][]provider.ToolDefinition{"root": effective.Root.Tools, "implementor": effective.Implementor.Tools, "auditor": effective.Auditor.Tools} {
				for _, def := range tools {
					if def.Name == "deep_research" || def.Name == "get_research_run" {
						t.Fatalf("%s received %s", role, def.Name)
					}
				}
			}
		})
	}
}

func readDeep(t *testing.T, s *harness.Session, q research.ReadQuery) []byte {
	t.Helper()
	return readDeepPages(t, func(q research.ReadQuery) (inspection.ResearchPage, error) {
		return s.ReadResearchReport(context.Background(), s.Root(), q)
	}, q)
}
func readDeepPages(t *testing.T, read func(research.ReadQuery) (inspection.ResearchPage, error), q research.ReadQuery) []byte {
	t.Helper()
	var body strings.Builder
	for {
		page, err := read(q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(page)
		if q.MaxBytes > 0 && len(b) > q.MaxBytes {
			t.Fatal("page exceeded cap")
		}
		if page.Fragment != nil {
			body.WriteString(page.Fragment.Text)
		} else {
			body.Write(page.Data)
		}
		if page.NextCursor == "" {
			break
		}
		q = research.ReadQuery{Mode: "continue", Cursor: page.NextCursor}
	}
	return []byte(body.String())
}
func TestDeepResearchToolArchiveAndAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := deepConfig(t)
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "deep.jsonl")
	worker := &deepWorker{receipt: make(chan string, 4)}
	model := &deepModel{}
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready"), Researcher: harness.AgentDependencies{Provider: worker}, DeepResearchProvider: model, ResearchWeb: deepWeb{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	for _, def := range s.Configuration().Root.Tools {
		if def.Name == "deep_research" {
			t.Fatal("root received blocking research tool")
		}
	}
	if s.Configuration().DeepResearch.Limits.Standard.ModelCalls != 40 {
		t.Fatal("effective configuration not resolved")
	}
	researcher := createWorker(t, s, roster.Researcher)
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: researcher, Task: "Measure"})
	if err != nil {
		t.Fatal(err)
	}
	receipt := await(t, worker.receipt, "deep research receipt")
	var digest struct {
		ID     string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(receipt), &digest); err != nil || digest.Status != "complete" {
		t.Fatal(receipt, err)
	}
	var report research.Report
	if err := json.Unmarshal(readDeep(t, s, research.ReadQuery{Mode: "run", RunID: digest.ID, MaxBytes: 2048}), &report); err != nil {
		t.Fatal(err)
	}
	if report.Binding.WorkID != string(w.ID) || len(report.Claims) != 1 || report.Spend.ModelCalls != 7 {
		t.Fatalf("bad retained report: %+v", report)
	}
	first, err := s.ReadResearchReport(ctx, researcher, research.ReadQuery{Mode: "source", RunID: digest.ID, SourceID: report.Sources[0].ID, MaxBytes: 2048})
	if err != nil || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	var source research.Source
	if err := json.Unmarshal(readDeep(t, s, research.ReadQuery{Mode: "source", RunID: digest.ID, SourceID: report.Sources[0].ID, MaxBytes: 2048}), &source); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source.Text, "12 milliseconds") {
		t.Fatal("lost source body")
	}
	service, err := httpapi.New(ctx, httpapi.Options{Authorize: httpapi.BearerToken("test"), Factory: func(context.Context, harness.Config) (*harness.Session, error) { return s, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		r.Header.Set("Authorization", "Bearer test")
		w := httptest.NewRecorder()
		service.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/sessions"); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, route := range []string{"research-view", "trace/research-view"} {
		base := "/sessions/" + s.ID() + "/" + route + "?actor=" + url.QueryEscape(string(s.Root()))
		w := call("GET", base+"&mode=source&run_id="+digest.ID+"&source_id="+source.ID+"&max_bytes=2048")
		var page inspection.ResearchPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.NextCursor == "" {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		if w := call("GET", base+"&mode=continue&cursor="+url.QueryEscape(page.NextCursor)); w.Code != 200 {
			t.Fatal("cursor did not survive next request", w.Code, w.Body.String())
		}
		if w := call("GET", base+"&mode=continue&cursor="+url.QueryEscape(page.NextCursor+"tampered")); w.Code != 400 {
			t.Fatal("tampered cursor accepted", w.Code)
		}
		if w := call("GET", base+"&mode=run&run_id="+digest.ID+"&unknown=value"); w.Code != 400 {
			t.Fatal("unknown selector accepted", w.Code)
		}
	}
	replacement := createWorker(t, s, roster.Researcher)
	if _, err = s.ReassignWork(ctx, s.Root(), work.ReassignRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Assignee: replacement}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadResearchReport(ctx, researcher, research.ReadQuery{Mode: "continue", Cursor: first.NextCursor}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal("continuation retained stale authorization", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ReadResearchReport(ctx, s.Root(), research.ReadQuery{Mode: "run", RunID: digest.ID}); err != nil || !strings.Contains(string(page.Data), "12 milliseconds") {
		t.Fatal("report lost after execution close", page, err)
	}
	// A ledger ID in run_id names its own reader instead of a bare not-found.
	if _, err := s.ReadResearchReport(ctx, s.Root(), research.ReadQuery{Mode: "run", RunID: "brief-1"}); !errors.Is(err, projection.ErrNotFound) || !strings.Contains(err.Error(), "read it with get_research_brief") {
		t.Fatal(err)
	}
	calls := model.calls.Load()
	archive, err := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(context.Background())
	reader, err := inspection.NewResearchReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Read(ctx, s.Root(), research.ReadQuery{Mode: "run", RunID: digest.ID})
	if err != nil || !strings.Contains(string(page.Data), "12 milliseconds") {
		t.Fatal(page, err)
	}
	if model.calls.Load() != calls {
		t.Fatal("archive restarted research")
	}
	// Cut a real accepted log at its last checkpoint to model process loss. Source
	// frames remain intact; the archive must never imply execution will continue.
	data, err := os.ReadFile(cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	var prefix bytes.Buffer
	lastCheckpoint := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		prefix.Write(line)
		prefix.WriteByte('\n')
		var rec eventlog.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Kind == "deep_research" && bytes.Contains(rec.Payload, []byte(`"kind":"checkpoint"`)) {
			lastCheckpoint = prefix.Len()
		}
	}
	if lastCheckpoint == 0 {
		t.Fatal("missing checkpoint fixture")
	}
	partialPath := filepath.Join(cfg.Dir, "interrupted-research.jsonl")
	if err := os.WriteFile(partialPath, prefix.Bytes()[:lastCheckpoint], 0600); err != nil {
		t.Fatal(err)
	}
	partial, err := inspection.OpenJSONL(ctx, partialPath)
	if err != nil {
		t.Fatal(err)
	}
	defer partial.Close(context.Background())
	partialReader, err := inspection.NewResearchReader(partial)
	if err != nil {
		t.Fatal(err)
	}
	page, err = partialReader.Read(ctx, s.Root(), research.ReadQuery{Mode: "run", RunID: digest.ID})
	if err != nil || !strings.Contains(string(page.Data), `"status":"incomplete"`) {
		t.Fatal(string(page.Data), err)
	}
}
func TestDeepResearchCancelReassignAndInterrupt(t *testing.T) {
	for _, mode := range []string{"cancel", "reassign", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			worker := &deepWorker{receipt: make(chan string, 4)}
			model := &deepModel{verifying: make(chan struct{})}
			rootCalled := make(chan struct{}, 1)
			s, err := harness.New(ctx, deepConfig(t), harness.Dependencies{Provider: responsiveDeepRoot{called: rootCalled}, Researcher: harness.AgentDependencies{Provider: worker}, DeepResearchProvider: model, ResearchWeb: deepWeb{}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Dispose(context.Background())
			researcher := createWorker(t, s, roster.Researcher)
			w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: researcher, Task: "Measure"})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-model.verifying:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if _, err = s.Send(s.Root(), "Still available?"); err != nil {
				t.Fatal("root unavailable", err)
			}
			select {
			case <-rootCalled:
			case <-ctx.Done():
				t.Fatal("root model was blocked by research", ctx.Err())
			}
			switch mode {
			case "cancel":
				_, err = s.CancelWork(ctx, s.Root(), work.CancelRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Reason: "Stop research"})
			case "reassign":
				replacement := createWorker(t, s, roster.Researcher)
				_, err = s.ReassignWork(ctx, s.Root(), work.ReassignRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Assignee: replacement})
			case "interrupt":
				err = s.Interrupt(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Close joins any remaining settlement without discarding accepted evidence.
			if err = s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			trace, err := s.Trace(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer trace.Close(context.Background())
			reader, err := inspection.NewResearchReader(trace)
			if err != nil {
				t.Fatal(err)
			}
			read := func(q research.ReadQuery) (inspection.ResearchPage, error) { return reader.Read(ctx, s.Root(), q) }
			data := readDeepPages(t, read, research.ReadQuery{Mode: "runs", WorkID: string(w.ID)})
			var runs []struct {
				ID     string `json:"run_id"`
				Status string `json:"status"`
			}
			if err = json.Unmarshal(data, &runs); err != nil || len(runs) != 1 {
				t.Fatal(string(data), err)
			}
			var report research.Report
			if err = json.Unmarshal(readDeepPages(t, read, research.ReadQuery{Mode: "run", RunID: runs[0].ID}), &report); err != nil {
				t.Fatal(err)
			}
			expected := "cancelled"
			if mode == "reassign" {
				expected = "reassigned"
			}
			if report.Status != "partial" || report.StopReason != expected || len(report.Sources) != 1 || len(report.Claims) != 0 || report.Binding.Actor != string(researcher) {
				t.Fatalf("bad cancellation result: %+v", report)
			}
		})
	}
}
