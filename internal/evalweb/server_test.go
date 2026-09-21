package evalweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a minimal ladder run and a nested interaction run so the
// discovery, analysis, trace and compare endpoints can be exercised without
// real traces: an empty trace analyses to zero metrics rather than an error.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	trace := `{"schema":5,"session":"s","sequence":1,"kind":"session_started","time":"2026-09-20T17:51:43Z","payload":{"id":"s"}}
{"schema":5,"session":"s","sequence":2,"kind":"agent_started","agent":"agent-1","time":"2026-09-20T17:51:44Z","payload":{"agent":{"agent_id":"agent-1"}}}
{"schema":5,"session":"s","sequence":3,"kind":"output_delta","agent":"agent-1","time":"2026-09-20T17:51:45Z","payload":{"text":"hi"}}
{"schema":5,"session":"s","sequence":4,"kind":"tool","agent":"agent-1","time":"2026-09-20T17:51:46Z","payload":{"name":"shell"}}
`
	for _, run := range []string{"a_qwen_20260920-100000", "cmp/b_qwen_20260921-100000"} {
		write(run+"/run.json", `{"started_at":"2026-09-20T10:00:00Z","commit":"abc1234","profile":"qwen","model":{"backend":"vllm","model":"qwen"},"tasks":["easy-01","hard-01"]}`)
		passed := "true"
		outcome := "passed"
		if strings.HasPrefix(run, "cmp") {
			passed, outcome = "false", "failed"
		}
		// The first easy-01 line is a retried attempt; the later graded line wins.
		write(run+"/results.jsonl", `{"task_id":"easy-01","tier":"easy","title":"One","outcome":"submitted","passed":false,"started_at":"2026-09-20T10:00:00Z","finished_at":"2026-09-20T10:01:00Z","duration_ns":60000000000,"trace":"x","workspace":"w","replies":1}
{"task_id":"easy-01","tier":"easy","title":"One","outcome":"`+outcome+`","passed":`+passed+`,"started_at":"2026-09-20T10:00:00Z","finished_at":"2026-09-20T10:01:00Z","duration_ns":60000000000,"trace":"x","workspace":"w","replies":1,"grade":{"passed":`+passed+`,"compiled":true,"command":"go test","output":"ok","duration_ns":1}}
{"task_id":"hard-01","tier":"hard","title":"Two","outcome":"submitted","passed":false,"started_at":"2026-09-20T10:00:00Z","finished_at":"2026-09-20T10:02:00Z","duration_ns":120000000000,"trace":"x","workspace":"w","replies":1}
`)
		write(run+"/easy-01/trace.jsonl", trace)
		write(run+"/hard-01/trace.jsonl", "")
		write(run+"/easy-01/workspace/main.go", "package main")
	}
	write("interaction-x/run.json", `{"version":1,"mode":"scripted","planned_trials":1,"results":null,"commit":"nogit","profile":"scripted"}`)
	write("interaction-x/results.jsonl", `{"scenario_id":"audit-independent","trial":1,"mode":"scripted","outcome":"passed","started_at":"2026-09-17T01:00:00Z","duration_ns":1000,"trace":"audit-independent/001/trace.jsonl","manifest":"audit-independent/001/manifest.json","start":{},"through":{},"stop_reason":"batch","model_calls":2,"tool_calls":3,"harness":{"passed":4,"total":4},"behavior":{"scorable":true,"outcome_correct":true,"clean_success":true,"recovery_success":false},"assertions":[]}
`)
	write("interaction-x/audit-independent/001/trace.jsonl", trace)
	return root
}

func get(t *testing.T, srv http.Handler, path string, into any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("%s: %v: %s", path, err, rec.Body.String())
		}
	}
	return rec
}

func TestDiscoveryGroupsNestedRunsAndTellsFamiliesApart(t *testing.T) {
	srv, err := New(fixture(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	var runs struct{ Runs []RunSummary }
	if rec := get(t, srv, "/api/runs", &runs); rec.Code != 200 || len(runs.Runs) != 3 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	byPath := map[string]RunSummary{}
	for _, r := range runs.Runs {
		byPath[r.Path] = r
	}
	a, b, i := byPath["a_qwen_20260920-100000"], byPath["cmp/b_qwen_20260921-100000"], byPath["interaction-x"]
	// The duplicate easy-01 line counts once; the submitted task is not graded.
	if a.Kind != Ladder || a.Tasks != 2 || a.Passed != 1 || a.Submitted != 1 || a.Model != "qwen" || a.Tiers["easy"].Passed != 1 {
		t.Fatalf("%+v", a)
	}
	if b.Group != "cmp" || b.Name != "b_qwen_20260921-100000" || b.Failed != 1 {
		t.Fatalf("%+v", b)
	}
	if i.Kind != Interaction || i.Trials == nil || i.Trials.Passed != 1 || i.Trials.Clean != 1 || i.Trials.Planned != 1 {
		t.Fatalf("%+v", i)
	}
}

func TestRunDetailAnalysesTracesAndServesTasks(t *testing.T) {
	srv, _ := New(fixture(t), nil)
	var detail ladderDetail
	if rec := get(t, srv, "/api/run?path=a_qwen_20260920-100000", &detail); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if len(detail.Report.Tasks) != 2 || detail.Report.Tasks[0].TaskID != "easy-01" || detail.Report.Tasks[0].Events != 4 {
		t.Fatalf("%+v", detail.Report.Tasks)
	}
	if detail.Results["easy-01"].Grade == nil || detail.Results["easy-01"].Grade.Output != "ok" {
		t.Fatal("latest result per task should carry its grade")
	}
	var inter interactionDetail
	if rec := get(t, srv, "/api/run?path=interaction-x", &inter); rec.Code != 200 || len(inter.Report.Results) != 1 {
		t.Fatal(rec.Body.String())
	}
}

func TestTracePagingFiltersAndHistogram(t *testing.T) {
	srv, _ := New(fixture(t), nil)
	var page TracePage
	get(t, srv, "/api/trace?run=a_qwen_20260920-100000&file=easy-01/trace.jsonl", &page)
	// output_delta is counted but hidden until asked for.
	if page.Total != 4 || page.Kinds["output_delta"] != 1 || len(page.Events) != 3 || page.Events[2].Kind != "tool" {
		t.Fatalf("%+v", page)
	}
	page = TracePage{}
	get(t, srv, "/api/trace?run=a_qwen_20260920-100000&file=easy-01/trace.jsonl&kinds=output_delta,tool&limit=1", &page)
	if len(page.Events) != 1 || page.Events[0].Kind != "output_delta" || page.Next != 3 {
		t.Fatalf("%+v", page)
	}
	page = TracePage{}
	get(t, srv, "/api/trace?run=a_qwen_20260920-100000&file=easy-01/trace.jsonl&after=3&agent=agent-1", &page)
	if len(page.Events) != 1 || page.Events[0].Sequence != 4 || page.Next != 0 {
		t.Fatalf("%+v", page)
	}
}

func TestCompareAndPathSafety(t *testing.T) {
	srv, _ := New(fixture(t), nil)
	var cmp struct{ Runs []ladderDetail }
	if rec := get(t, srv, "/api/compare?run=a_qwen_20260920-100000&run=cmp/b_qwen_20260921-100000", &cmp); rec.Code != 200 || len(cmp.Runs) != 2 || cmp.Runs[1].Summary.Failed != 1 {
		t.Fatal(rec.Body.String())
	}
	if rec := get(t, srv, "/api/compare?run=a_qwen_20260920-100000&run=interaction-x", nil); rec.Code != 400 {
		t.Fatal("interaction runs must be refused by compare", rec.Code)
	}
	for _, path := range []string{"/api/run?path=../etc", "/api/run?path=/etc", "/api/run?path=missing", "/api/trace?run=a_qwen_20260920-100000&file=../run.json", "/api/trace?run=a_qwen_20260920-100000&file=/etc/passwd"} {
		if rec := get(t, srv, path, nil); rec.Code == 200 {
			t.Fatal("served outside the run", path)
		}
	}
	if rec := get(t, srv, "/", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "strap eval") {
		t.Fatal("page not served", rec.Code)
	}
}
