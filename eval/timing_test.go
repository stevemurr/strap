package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A small hand-built trace: the manager thinks for 2s and assigns work, the
// implementor picks it up, calls the model for 3s while the manager's second
// call overlaps it by 1s, runs a 1s shell, and submits.
func TestTimingPartitionsTheWallClock(t *testing.T) {
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	at := func(ms int) string { return base.Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339Nano) }
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	add(`{"kind":"session_started","time":%q,"payload":{}}`, at(0))
	add(`{"kind":"agent_registered","time":%q,"payload":{"registration":{"agent_id":"agent-1","role":"manager"}}}`, at(0))
	add(`{"kind":"agent_registered","time":%q,"payload":{"registration":{"agent_id":"agent-2","role":"implementor"}}}`, at(0))
	add(`{"kind":"agent_state","time":%q,"payload":{"agent":"agent-1","state":"running"}}`, at(0))
	add(`{"kind":"output_started","time":%q,"output":{"agent":"agent-1","call":1},"payload":{}}`, at(0))
	add(`{"kind":"output_delta","time":%q,"output":{"agent":"agent-1","call":1},"payload":{"channel":"reasoning"}}`, at(500))
	add(`{"kind":"output_delta","time":%q,"output":{"agent":"agent-1","call":1},"payload":{"channel":"content"}}`, at(1500))
	add(`{"kind":"usage","time":%q,"payload":{"agent":"agent-1","observation":{"call":1,"usage":{"input_tokens":1000,"output_tokens":100,"cached_tokens":800,"reasoning_tokens":60}}}}`, at(2000))
	add(`{"kind":"output_finished","time":%q,"output":{"agent":"agent-1","call":1},"payload":{"status":"completed"}}`, at(2000))
	add(`{"kind":"tool","time":%q,"payload":{"agent":"agent-1","activity":{"name":"assign_task","started_at":%q,"finished_at":%q}}}`, at(2100), at(2100), at(2200))
	add(`{"kind":"work","time":%q,"payload":{"event":{"change":{"works":[{"work_id":"work-1","kind":"implementation","state":"active","assignee":"agent-2"}]}}}}`, at(2200))
	add(`{"kind":"agent_state","time":%q,"payload":{"agent":"agent-2","state":"running"}}`, at(2300))
	add(`{"kind":"output_started","time":%q,"output":{"agent":"agent-2","call":1},"payload":{}}`, at(2400))
	add(`{"kind":"output_started","time":%q,"output":{"agent":"agent-1","call":2},"payload":{}}`, at(4400))
	add(`{"kind":"output_finished","time":%q,"output":{"agent":"agent-1","call":2},"payload":{"status":"completed"}}`, at(5400))
	add(`{"kind":"agent_state","time":%q,"payload":{"agent":"agent-1","state":"idle"}}`, at(5400))
	add(`{"kind":"output_finished","time":%q,"output":{"agent":"agent-2","call":1},"payload":{"status":"completed"}}`, at(5400))
	add(`{"kind":"tool","time":%q,"payload":{"agent":"agent-2","activity":{"name":"shell","started_at":%q,"finished_at":%q,"error":"exit 1"}}}`, at(6400), at(5400), at(6400))
	add(`{"kind":"work","time":%q,"payload":{"event":{"change":{"works":[{"work_id":"work-1","kind":"implementation","state":"needs_check","assignee":"agent-2"}]}}}}`, at(6500))
	add(`{"kind":"agent_state","time":%q,"payload":{"agent":"agent-2","state":"idle"}}`, at(6500))
	add(`{"kind":"server_metrics","time":%q,"payload":{"phase":"start","values":{"vllm:prefix_cache_hits_total":100,"vllm:prefix_cache_queries_total":1000,"vllm:request_queue_time_seconds_sum":1,"vllm:request_queue_time_seconds_count":10,"vllm:num_requests_waiting":0}}}`, at(0))
	add(`{"kind":"server_metrics","time":%q,"payload":{"phase":"sample","values":{"vllm:num_requests_waiting":3,"vllm:kv_cache_usage_perc":0.4}}}`, at(3000))
	add(`{"kind":"server_metrics","time":%q,"payload":{"phase":"end","values":{"vllm:prefix_cache_hits_total":700,"vllm:prefix_cache_queries_total":2000,"vllm:request_queue_time_seconds_sum":1.5,"vllm:request_queue_time_seconds_count":14,"vllm:num_requests_waiting":1}}}`, at(7000))
	add(`{"kind":"session_closed","time":%q,"payload":{}}`, at(7000))
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm, err := AnalyzeTiming(path)
	if err != nil {
		t.Fatal(err)
	}
	ms := func(d time.Duration) int { return int(d / time.Millisecond) }
	// model: 0–2000 and 2400–5400; tool: 2100–2200, 5400–6400; harness: the
	// running gaps 2000–2100, 2200–2400, 6400–6500; idle: 6500–7000.
	want := map[string]int{"model": 5000, "tool": 1100, "harness": 400, "idle": 500}
	var sum time.Duration
	for bucket, w := range want {
		if got := ms(tm.Buckets[bucket]); got != w {
			t.Errorf("%s bucket %dms, want %dms", bucket, got, w)
		}
	}
	for _, d := range tm.Buckets {
		sum += d
	}
	if sum != tm.Wall || ms(tm.Wall) != 7000 {
		t.Errorf("buckets sum to %v of %v", sum, tm.Wall)
	}
	// The manager's second call shared 1s with the implementor's.
	if ms(tm.ModelWall["manager"]) != 2500 || ms(tm.ModelWall["implementor"]) != 2500 {
		t.Errorf("model wall %v", tm.ModelWall)
	}
	first := tm.Models[0]
	if ms(first.Prefill) != 500 || ms(first.Reasoning) != 1000 || ms(first.Answer) != 500 || first.InputTokens != 1000 || first.CachedTokens != 800 || first.ReasoningTokens != 60 {
		t.Errorf("manager call %+v", first)
	}
	if len(tm.Tools) != 2 || !tm.Tools[1].Failed {
		t.Errorf("tools %+v", tm.Tools)
	}
	stages := map[string]int{}
	for _, s := range tm.Stages {
		stages[s.Name] = ms(s.Duration)
	}
	if stages["worker picks up implementation"] != 200 || stages["implementation active until submitted"] != 4300 {
		t.Errorf("stages %v", stages)
	}
	if sv := tm.Server; sv == nil || sv.Samples != 3 || sv.Deltas["vllm:prefix_cache_hits_total"] != 600 || sv.Peak["vllm:num_requests_waiting"] != 3 {
		t.Errorf("server %+v", tm.Server)
	}
	// The manager's calls and tool are coordination; the implementor worked
	// under its implementation from 2.2s; the 1s overlap is shared.
	for phase, w := range map[string]int{"manager": 2600, "implementation": 3500, "harness": 400, "idle": 500} {
		if got := ms(tm.Phases[phase]); got != w {
			t.Errorf("%s phase %dms, want %dms", phase, got, w)
		}
	}
	report := TimingMarkdown([]Timing{tm})
	// The manager reported cached and reasoning tokens; the implementor did not.
	for _, want := range []string{"| model | 5s | 71.4% |", "| shell | 1 |", "| implementor | 1 |", "| 500 | 80% | 50 | 60% |", "| 0 | — | 0 | — |", "| Prefix cache hits (tokens) | 60.0% (600 of 1000) |", "| Queue time (waiting to be scheduled) | 500ms total, 125ms mean over 4 requests |", "| Peak requests running / waiting | 0 / 3 |", "| implementation | 3.5s | 50.0% |"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}
