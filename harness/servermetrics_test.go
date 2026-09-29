package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeMetrics = `# HELP vllm:prefix_cache_hits_total Prefix cache hits.
# TYPE vllm:prefix_cache_hits_total counter
vllm:prefix_cache_hits_total{engine="0",model_name="qwen3.6"} 12864.0
vllm:prefix_cache_queries_total{engine="0",model_name="qwen3.6"} 29490.0
vllm:num_requests_waiting{engine="0",model_name="qwen3.6"} 1.0
vllm:num_requests_waiting{engine="1",model_name="qwen3.6"} 2.0
vllm:request_queue_time_seconds_sum{engine="0",model_name="qwen3.6"} 0.25
vllm:request_queue_time_seconds_bucket{engine="0",le="0.3",model_name="qwen3.6"} 4.0
vllm:kv_cache_usage_perc 0.5
vllm:unrelated_total 7
`

func TestParseServerMetricsKeepsKnownSeriesSummedAcrossLabels(t *testing.T) {
	v := parseServerMetrics(strings.NewReader(fakeMetrics))
	want := map[string]float64{"vllm:prefix_cache_hits_total": 12864, "vllm:prefix_cache_queries_total": 29490, "vllm:num_requests_waiting": 3, "vllm:request_queue_time_seconds_sum": 0.25, "vllm:kv_cache_usage_perc": 0.5}
	if len(v) != len(want) {
		t.Fatalf("%v", v)
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %v, want %v", k, v[k], w)
		}
	}
}

// A session that talks to a vLLM server itself records the server's metrics
// at start, while it runs, and at close.
func TestSessionRecordsServerMetrics(t *testing.T) {
	var scrapes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		scrapes.Add(1)
		_, _ = w.Write([]byte(fakeMetrics))
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LSP = t.TempDir(), nil, nil
	cfg.Model.BaseURL = server.URL + "/v1"
	cfg.Telemetry.ContextTokens, cfg.Telemetry.ServerMetrics = false, true
	cfg.Telemetry.ServerMetricsInterval = 100 * time.Millisecond
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	s, err := New(context.Background(), cfg, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	trace := string(data)
	for _, phase := range []string{`"phase":"start"`, `"phase":"sample"`, `"phase":"end"`} {
		if !strings.Contains(trace, phase) {
			t.Errorf("trace lacks a %s snapshot", phase)
		}
	}
	if !strings.Contains(trace, `"source":"`+server.URL+`/metrics"`) || !strings.Contains(trace, `"vllm:prefix_cache_hits_total":12864`) {
		t.Error("snapshot lacks the source or its values")
	}
	if scrapes.Load() < 3 {
		t.Errorf("%d scrapes", scrapes.Load())
	}
}
