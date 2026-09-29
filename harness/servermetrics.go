package harness

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/conversation"
)

// serverMetricNames are the vLLM series a session records. Counters and
// histogram sums/counts are compared between the first and last snapshot;
// the gauges are sampled for their peak and mean. Series with labels are
// summed across them.
var serverMetricNames = map[string]bool{
	"vllm:request_queue_time_seconds_sum":        true,
	"vllm:request_queue_time_seconds_count":      true,
	"vllm:request_prefill_time_seconds_sum":      true,
	"vllm:request_prefill_time_seconds_count":    true,
	"vllm:request_decode_time_seconds_sum":       true,
	"vllm:request_decode_time_seconds_count":     true,
	"vllm:time_to_first_token_seconds_sum":       true,
	"vllm:time_to_first_token_seconds_count":     true,
	"vllm:e2e_request_latency_seconds_sum":       true,
	"vllm:e2e_request_latency_seconds_count":     true,
	"vllm:prefix_cache_queries_total":            true,
	"vllm:prefix_cache_hits_total":               true,
	"vllm:prompt_tokens_total":                   true,
	"vllm:prompt_tokens_cached_total":            true,
	"vllm:generation_tokens_total":               true,
	"vllm:spec_decode_num_drafts_total":          true,
	"vllm:spec_decode_num_draft_tokens_total":    true,
	"vllm:spec_decode_num_accepted_tokens_total": true,
	"vllm:num_preemptions_total":                 true,
	"vllm:request_success_total":                 true,
	"vllm:num_requests_running":                  true,
	"vllm:num_requests_waiting":                  true,
	"vllm:kv_cache_usage_perc":                   true,
}

// serverMetrics samples the model server's /metrics into the trace. It
// never fails the session: an unreachable endpoint is recorded once and
// then left alone.
type serverMetrics struct {
	session  *Session
	source   string
	client   *http.Client
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	failed   bool
}

// startServerMetrics samples the session's vLLM server, when the session
// talks to one itself.
func startServerMetrics(s *Session, cfg Config, deps Dependencies) *serverMetrics {
	if !cfg.Telemetry.ServerMetrics || deps.Provider != nil || deps.Manager.Provider != nil || deps.Agent.Provider != nil || cfg.Manager.Model != nil || cfg.Agent.Model != nil {
		return nil
	}
	model, err := cfg.Model.Resolve()
	if err != nil || model.Backend != "vllm" {
		return nil
	}
	u, err := url.Parse(model.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	u.User, u.Path, u.RawQuery, u.Fragment = nil, "/metrics", "", ""
	m := &serverMetrics{session: s, source: u.String(), client: &http.Client{Timeout: 3 * time.Second}, interval: cfg.Telemetry.ServerMetricsInterval, stop: make(chan struct{}), done: make(chan struct{})}
	m.sample("start")
	go m.run()
	return m
}

func (m *serverMetrics) run() {
	defer close(m.done)
	tick := time.NewTicker(m.interval)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			m.sample("sample")
		}
	}
}

// finish stops sampling and records the last snapshot.
func (m *serverMetrics) finish() {
	if m == nil {
		return
	}
	m.once.Do(func() {
		close(m.stop)
		<-m.done
		m.sample("end")
	})
}

func (m *serverMetrics) sample(phase string) {
	if m.failed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.client.Timeout)
	defer cancel()
	values, err := scrapeServerMetrics(ctx, m.client, m.source)
	e := conversation.ServerMetricsEvent{Source: m.source, Phase: phase, Values: values}
	if err != nil {
		m.failed = true
		e.Values, e.Error = nil, err.Error()
	}
	_ = m.session.publish(e)
}

func scrapeServerMetrics(ctx context.Context, client *http.Client, source string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &url.Error{Op: "GET", URL: source, Err: io.ErrUnexpectedEOF}
	}
	return parseServerMetrics(resp.Body), nil
}

// parseServerMetrics reads the Prometheus text format, keeping the series in
// serverMetricNames and summing each across its labels.
func parseServerMetrics(r io.Reader) map[string]float64 {
	values := map[string]float64{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name := line
		if i := strings.IndexAny(line, "{ "); i >= 0 {
			name = line[:i]
		}
		if !serverMetricNames[name] {
			continue
		}
		rest := line[len(name):]
		if strings.HasPrefix(rest, "{") {
			rest = rest[strings.LastIndex(rest, "}")+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
			values[name] += v
		}
	}
	return values
}
