package eval

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Timing says where a session's wall-clock time went: model calls by role,
// tools by name, the harness between them, and nothing at all. It reads a
// recorded trace directly; every record carries its time.
type Timing struct {
	Task  string        `json:"task,omitempty"`
	Trace string        `json:"trace"`
	Wall  time.Duration `json:"wall_ns"`
	// Buckets partition the wall clock by what was running at each moment.
	Buckets map[string]time.Duration `json:"buckets_ns"`
	// ModelWall and ToolWall split the model and tool buckets' time among the
	// roles and tools running at once, so each sums to its buckets.
	ModelWall  map[string]time.Duration `json:"model_wall_ns"`
	ToolWall   map[string]time.Duration `json:"tool_wall_ns"`
	Models     []ModelCall              `json:"models"`
	Tools      []ToolCall               `json:"tools"`
	Gaps       []Gap                    `json:"gaps"`
	Stages     []Stage                  `json:"stages"`
	Waiting    time.Duration            `json:"waiting_ns"` // Summed wait_for_input time.
	Concurrent float64                  `json:"concurrent"` // Mean model calls in flight while any is.
	Server     *ServerStats             `json:"server,omitempty"`
	// Phases split the wall clock by the work being done at each moment: the
	// kind of work item the active agent holds, or the manager coordinating.
	// Overlapping moments are shared; harness and idle time keep their names.
	Phases map[string]time.Duration `json:"phases_ns"`
}

// ServerStats is what the model server's own metrics say about the session:
// counter deltas between its first and last snapshot, and the peaks and means
// of its gauges. They are server-wide, so they describe the session only while
// it was the server's sole client.
type ServerStats struct {
	Samples int                `json:"samples"`
	Deltas  map[string]float64 `json:"deltas"`
	Peak    map[string]float64 `json:"peak"`
	Mean    map[string]float64 `json:"mean"`
}

var serverGauges = []string{"vllm:num_requests_running", "vllm:num_requests_waiting", "vllm:kv_cache_usage_perc"}

// The wall-clock buckets, in the order they are reported.
var timingBuckets = []string{"model", "model+tool", "tool", "harness", "idle"}

type ModelCall struct {
	Agent        string        `json:"agent"`
	Role         string        `json:"role"`
	Duration     time.Duration `json:"duration_ns"`
	Prefill      time.Duration `json:"prefill_ns"`   // Start to first streamed token: queueing and prompt processing.
	Reasoning    time.Duration `json:"reasoning_ns"` // First reasoning token to the answer.
	Answer       time.Duration `json:"answer_ns"`    // First answer token to the end.
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	// CachedTokens and ReasoningTokens are -1 when the server did not report them.
	CachedTokens    int64  `json:"cached_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	Status          string `json:"status"`
}
type ToolCall struct {
	Agent    string        `json:"agent"`
	Role     string        `json:"role"`
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration_ns"`
	Failed   bool          `json:"failed"`
}

// Gap is time an agent was running but neither calling the model nor a tool:
// the harness between its steps. After a model call it dispatches tools;
// after tools it builds the next request.
type Gap struct {
	Agent    string        `json:"agent"`
	Kind     string        `json:"kind"` // "dispatch" (model → tool) or "resume" (tool → model).
	Duration time.Duration `json:"duration_ns"`
}

// Stage is one latency in the work protocol, such as a worker picking up its
// assignment or the manager assigning an audit.
type Stage struct {
	Name     string        `json:"name"`
	Duration time.Duration `json:"duration_ns"`
}

type timingRecord struct {
	Kind   string    `json:"kind"`
	Agent  string    `json:"agent"`
	Time   time.Time `json:"time"`
	Output *struct {
		Agent string `json:"agent"`
		Call  uint64 `json:"call"`
	} `json:"output"`
	Payload json.RawMessage `json:"payload"`
}

type span struct {
	start, end  time.Time
	agent, kind string // kind: model, tool or wait.
	name        string
	phase       string
}

// hold is an agent holding a work item of a kind between two moments.
type hold struct {
	agent, kind string
	from, to    time.Time
}

type outputKey struct {
	agent string
	call  uint64
}

type modelState struct {
	start, firstDelta, firstReasoning, firstContent, end time.Time
	status                                               string
	in, out                                              int64
	cached, reasoning                                    *int64
}

// AnalyzeTiming reads one trace.
func AnalyzeTiming(path string) (Timing, error) {
	f, err := os.Open(path)
	if err != nil {
		return Timing{}, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		z, err := gzip.NewReader(f)
		if err != nil {
			return Timing{}, err
		}
		defer z.Close()
		r = z
	}
	t := Timing{Trace: path, Buckets: map[string]time.Duration{}, ModelWall: map[string]time.Duration{}, ToolWall: map[string]time.Duration{}, Phases: map[string]time.Duration{}}
	roles := map[string]string{}
	models := map[outputKey]*modelState{}
	var order []outputKey
	var spans []span
	running := map[string]time.Time{}
	var runs []span
	var first, last time.Time
	var manager string
	var managerStarts []time.Time
	var managerMessages []time.Time
	type workState struct {
		kind, assignee, parent string
		assigned, submitted    time.Time
		pickedUp               bool
	}
	works := map[string]*workState{}
	open := map[string]*hold{}
	var holds []hold
	var serverFirst, serverLast map[string]float64
	server := &ServerStats{Deltas: map[string]float64{}, Peak: map[string]float64{}, Mean: map[string]float64{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var rec timingRecord
		if json.Unmarshal(scanner.Bytes(), &rec) != nil || rec.Time.IsZero() {
			continue
		}
		if first.IsZero() {
			first = rec.Time
		}
		last = rec.Time
		switch rec.Kind {
		case "agent_registered":
			var p struct {
				Registration struct {
					AgentID string `json:"agent_id"`
					Role    string `json:"role"`
				} `json:"registration"`
			}
			if json.Unmarshal(rec.Payload, &p) == nil {
				roles[p.Registration.AgentID] = p.Registration.Role
				if p.Registration.Role == "manager" {
					manager = p.Registration.AgentID
				}
			}
		case "agent_state":
			var p struct {
				Agent string `json:"agent"`
				State string `json:"state"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil {
				continue
			}
			if p.State == "running" {
				if _, ok := running[p.Agent]; !ok {
					running[p.Agent] = rec.Time
				}
			} else if start, ok := running[p.Agent]; ok {
				runs = append(runs, span{start: start, end: rec.Time, agent: p.Agent})
				delete(running, p.Agent)
			}
		case "output_started":
			if rec.Output == nil {
				continue
			}
			k := outputKey{rec.Output.Agent, rec.Output.Call}
			models[k] = &modelState{start: rec.Time}
			order = append(order, k)
			if rec.Output.Agent == manager {
				managerStarts = append(managerStarts, rec.Time)
			}
			for _, w := range works {
				if !w.pickedUp && w.assignee == rec.Output.Agent && !w.assigned.IsZero() && rec.Time.After(w.assigned) {
					w.pickedUp = true
					t.Stages = append(t.Stages, Stage{Name: "worker picks up " + w.kind, Duration: rec.Time.Sub(w.assigned)})
				}
			}
		case "output_delta":
			if rec.Output == nil {
				continue
			}
			m := models[outputKey{rec.Output.Agent, rec.Output.Call}]
			if m == nil {
				continue
			}
			var p struct {
				Channel string `json:"channel"`
			}
			_ = json.Unmarshal(rec.Payload, &p)
			if m.firstDelta.IsZero() {
				m.firstDelta = rec.Time
			}
			if p.Channel == "reasoning" && m.firstReasoning.IsZero() {
				m.firstReasoning = rec.Time
			}
			if p.Channel != "reasoning" && m.firstContent.IsZero() {
				m.firstContent = rec.Time
			}
		case "output_finished":
			if rec.Output == nil {
				continue
			}
			m := models[outputKey{rec.Output.Agent, rec.Output.Call}]
			if m == nil {
				continue
			}
			var p struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(rec.Payload, &p)
			m.end, m.status = rec.Time, p.Status
		case "usage":
			var p struct {
				Agent       string `json:"agent"`
				Observation struct {
					Call  uint64 `json:"call"`
					Usage *struct {
						In        *int64 `json:"input_tokens"`
						Out       *int64 `json:"output_tokens"`
						Cached    *int64 `json:"cached_tokens"`
						Reasoning *int64 `json:"reasoning_tokens"`
					} `json:"usage"`
				} `json:"observation"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil || p.Observation.Usage == nil {
				continue
			}
			if m := models[outputKey{p.Agent, p.Observation.Call}]; m != nil {
				if u := p.Observation.Usage; u.In != nil {
					m.in = *u.In
				}
				if u := p.Observation.Usage; u.Out != nil {
					m.out = *u.Out
				}
				m.cached, m.reasoning = p.Observation.Usage.Cached, p.Observation.Usage.Reasoning
			}
		case "tool":
			var p struct {
				Agent    string `json:"agent"`
				Activity struct {
					Name       string    `json:"name"`
					StartedAt  time.Time `json:"started_at"`
					FinishedAt time.Time `json:"finished_at"`
					Error      string    `json:"error"`
				} `json:"activity"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil || p.Activity.FinishedAt.Year() < 2000 {
				continue
			}
			a := p.Activity
			kind := "tool"
			if a.Name == "wait_for_input" {
				kind = "wait"
				t.Waiting += a.FinishedAt.Sub(a.StartedAt)
			} else {
				t.Tools = append(t.Tools, ToolCall{Agent: p.Agent, Role: roles[p.Agent], Name: a.Name, Duration: a.FinishedAt.Sub(a.StartedAt), Failed: a.Error != ""})
			}
			spans = append(spans, span{start: a.StartedAt, end: a.FinishedAt, agent: p.Agent, kind: kind, name: a.Name})
		case "server_metrics":
			var p struct {
				Values map[string]float64 `json:"values"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil || len(p.Values) == 0 {
				continue
			}
			if serverFirst == nil {
				serverFirst = p.Values
			}
			serverLast = p.Values
			server.Samples++
			for _, g := range serverGauges {
				v := p.Values[g]
				server.Peak[g] = max(server.Peak[g], v)
				server.Mean[g] += v
			}
		case "message":
			var p struct {
				Message struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"message"`
			}
			if json.Unmarshal(rec.Payload, &p) == nil && manager != "" && p.Message.To == manager && p.Message.From != manager {
				managerMessages = append(managerMessages, rec.Time)
			}
		case "work":
			var p struct {
				Event struct {
					Change struct {
						Works []struct {
							ID       string `json:"work_id"`
							Kind     string `json:"kind"`
							State    string `json:"state"`
							Assignee string `json:"assignee"`
							Parent   string `json:"parent_id"`
						} `json:"works"`
					} `json:"change"`
				} `json:"event"`
			}
			if json.Unmarshal(rec.Payload, &p) != nil {
				continue
			}
			for _, w := range p.Event.Change.Works {
				if h := open[w.ID]; h != nil && (w.State != "active" || h.agent != w.Assignee) {
					h.to = rec.Time
					holds = append(holds, *h)
					delete(open, w.ID)
				}
				if w.State == "active" && open[w.ID] == nil {
					open[w.ID] = &hold{agent: w.Assignee, kind: w.Kind, from: rec.Time}
				}
				s := works[w.ID]
				if s == nil {
					s = &workState{kind: w.Kind, parent: w.Parent}
					works[w.ID] = s
				}
				switch w.State {
				case "active":
					if s.assigned.IsZero() || s.assignee != w.Assignee {
						s.assigned, s.assignee, s.pickedUp = rec.Time, w.Assignee, false
						if w.Kind == "audit" {
							if parent := works[w.Parent]; parent != nil && !parent.submitted.IsZero() {
								t.Stages = append(t.Stages, Stage{Name: "manager assigns audit after submission", Duration: rec.Time.Sub(parent.submitted)})
							}
						}
					}
				case "needs_check":
					if s.submitted.IsZero() || w.Kind != "audit" {
						s.submitted = rec.Time
						if !s.assigned.IsZero() {
							t.Stages = append(t.Stages, Stage{Name: s.kind + " active until submitted", Duration: rec.Time.Sub(s.assigned)})
						}
					}
				case "delivered", "closed":
					if !s.assigned.IsZero() {
						t.Stages = append(t.Stages, Stage{Name: s.kind + " active until " + w.State, Duration: rec.Time.Sub(s.assigned)})
						s.assigned = time.Time{}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Timing{}, err
	}
	for _, h := range open {
		h.to = last
		holds = append(holds, *h)
	}
	for agent, start := range running {
		runs = append(runs, span{start: start, end: last, agent: agent})
	}
	for _, k := range order {
		m := models[k]
		if m.end.IsZero() {
			m.end = last
		}
		c := ModelCall{Agent: k.agent, Role: roles[k.agent], Duration: m.end.Sub(m.start), InputTokens: m.in, OutputTokens: m.out, CachedTokens: -1, ReasoningTokens: -1, Status: m.status}
		if m.cached != nil {
			c.CachedTokens = *m.cached
		}
		if m.reasoning != nil {
			c.ReasoningTokens = *m.reasoning
		}
		if !m.firstDelta.IsZero() {
			c.Prefill = m.firstDelta.Sub(m.start)
		}
		if !m.firstReasoning.IsZero() {
			answer := m.end
			if !m.firstContent.IsZero() && m.firstContent.After(m.firstReasoning) {
				answer = m.firstContent
			}
			c.Reasoning = answer.Sub(m.firstReasoning)
		}
		if !m.firstContent.IsZero() && (m.firstReasoning.IsZero() || m.firstContent.After(m.firstReasoning)) {
			c.Answer = m.end.Sub(m.firstContent)
		}
		t.Models = append(t.Models, c)
		spans = append(spans, span{start: m.start, end: m.end, agent: k.agent, kind: "model", name: roles[k.agent]})
	}
	for i := range spans {
		spans[i].phase = phaseOf(spans[i], roles, holds)
	}
	t.Wall = last.Sub(first)
	if server.Samples > 0 {
		for k, v := range serverLast {
			if !slices.Contains(serverGauges, k) {
				server.Deltas[k] = v - serverFirst[k]
			}
		}
		for _, g := range serverGauges {
			server.Mean[g] /= float64(server.Samples)
		}
		t.Server = server
	}
	t.Gaps = harnessGaps(spans, runs)
	t.sweep(spans, runs, first, last)
	for _, at := range managerMessages {
		for _, s := range managerStarts {
			if s.After(at) {
				t.Stages = append(t.Stages, Stage{Name: "manager's next call after a message", Duration: s.Sub(at)})
				break
			}
		}
	}
	return t, nil
}

// sweep partitions [first, last] by what was running at each moment and
// splits the model and tool buckets among what shared them.
func (t *Timing) sweep(spans, runs []span, first, last time.Time) {
	type edge struct {
		at    time.Time
		delta int
		s     span
	}
	var edges []edge
	for _, s := range append(slicesOf(spans), runs...) {
		if !s.end.After(s.start) {
			continue
		}
		edges = append(edges, edge{s.start, 1, s}, edge{s.end, -1, s})
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if !edges[i].at.Equal(edges[j].at) {
			return edges[i].at.Before(edges[j].at)
		}
		return edges[i].delta < edges[j].delta // Close before open at the same instant.
	})
	active := map[span]bool{}
	prev := first
	var inFlight, busy float64
	for _, e := range append(edges, edge{at: last}) {
		if d := e.at.Sub(prev); d > 0 {
			var models, tools []span
			runningAgents := map[string]bool{}
			waiting := map[string]bool{}
			for s := range active {
				switch s.kind {
				case "model":
					models = append(models, s)
				case "tool":
					tools = append(tools, s)
				case "wait":
					waiting[s.agent] = true
				case "":
					runningAgents[s.agent] = true
				}
			}
			harness := false
			for a := range runningAgents {
				harness = harness || !waiting[a]
			}
			bucket := "idle"
			switch {
			case len(models) > 0 && len(tools) > 0:
				bucket = "model+tool"
			case len(models) > 0:
				bucket = "model"
			case len(tools) > 0:
				bucket = "tool"
			case harness:
				bucket = "harness"
			}
			t.Buckets[bucket] += d
			phases := map[string]bool{}
			for _, s := range append(models, tools...) {
				phases[s.phase] = true
			}
			if len(phases) == 0 {
				phases[bucket] = true // harness or idle
			}
			for ph := range phases {
				t.Phases[ph] += d / time.Duration(len(phases))
			}
			if len(models) > 0 {
				inFlight += float64(len(models)) * float64(d)
				busy += float64(d)
			}
			// The model bucket is split among the roles generating; the tool
			// bucket among the tools running; a shared moment goes to tools.
			share := models
			into := t.ModelWall
			if len(tools) > 0 {
				share, into = tools, t.ToolWall
			}
			for _, s := range share {
				into[s.name] += d / time.Duration(len(share))
			}
		}
		prev = e.at
		if e.delta > 0 {
			active[e.s] = true
		} else if e.delta < 0 {
			delete(active, e.s)
		}
	}
	if busy > 0 {
		t.Concurrent = inFlight / busy
	}
}

func slicesOf(s []span) []span { return append([]span(nil), s...) }

// phaseOf names the work a span served: the manager coordinating, or the
// kind of work item its agent held when it started. Web and deep research are
// both research; traces from before the review kind recorded a reviewer's work
// as research, which is a review. A worker
// holding nothing is giving its closing reply after submitting: its wrap-up.
func phaseOf(s span, roles map[string]string, holds []hold) string {
	role := roles[s.agent]
	if role == "manager" || role == "debugger" {
		return role
	}
	for _, h := range holds {
		if h.agent == s.agent && !s.start.Before(h.from) && s.start.Before(h.to) {
			switch {
			case h.kind == "research" && role == "reviewer":
				return "review"
			case h.kind == "web_research" || h.kind == "deep_research":
				return "research"
			}
			return h.kind
		}
	}
	if role == "" {
		return "unattributed"
	}
	return role + " wrap-up" // Its closing reply after submitting.
}

// harnessGaps finds, for each agent, the time between one of its steps
// ending and its next starting while it was running.
func harnessGaps(spans, runs []span) []Gap {
	byAgent := map[string][]span{}
	for _, s := range spans {
		byAgent[s.agent] = append(byAgent[s.agent], s)
	}
	var gaps []Gap
	for agent, ss := range byAgent {
		sort.Slice(ss, func(i, j int) bool { return ss[i].start.Before(ss[j].start) })
		for i := 1; i < len(ss); i++ {
			prev, next := ss[i-1], ss[i]
			d := next.start.Sub(prev.end)
			if d <= 0 || !within(runs, agent, prev.end, next.start) {
				continue
			}
			kind := "resume"
			if prev.kind == "model" {
				kind = "dispatch"
			}
			gaps = append(gaps, Gap{Agent: agent, Kind: kind, Duration: d})
		}
	}
	return gaps
}

// within reports whether agent was running for all of [from, to].
func within(runs []span, agent string, from, to time.Time) bool {
	for _, r := range runs {
		if r.agent == agent && !r.start.After(from) && !r.end.Before(to) {
			return true
		}
	}
	return false
}

// AnalyzeTimingDir reads every trace under dir: a trace file, a run's results
// directory, or a batch of them.
func AnalyzeTimingDir(dir string) ([]Timing, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		t, err := AnalyzeTiming(dir)
		return []Timing{t}, err
	}
	var paths []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (d.Name() == "trace.jsonl" || d.Name() == "trace.jsonl.gz") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []Timing
	for _, p := range paths {
		t, err := AnalyzeTiming(p)
		if err != nil {
			return out, fmt.Errorf("%s: %w", p, err)
		}
		t.Task = filepath.Base(filepath.Dir(p))
		out = append(out, t)
	}
	return out, nil
}

// TimingMarkdown reports the traces together, then each on its own line.
func TimingMarkdown(ts []Timing) string {
	var b strings.Builder
	total := Timing{Buckets: map[string]time.Duration{}, ModelWall: map[string]time.Duration{}, ToolWall: map[string]time.Duration{}, Phases: map[string]time.Duration{}}
	var concurrent float64
	for _, t := range ts {
		total.Wall += t.Wall
		total.Waiting += t.Waiting
		for k, v := range t.Buckets {
			total.Buckets[k] += v
		}
		for k, v := range t.ModelWall {
			total.ModelWall[k] += v
		}
		for k, v := range t.ToolWall {
			total.ToolWall[k] += v
		}
		for k, v := range t.Phases {
			total.Phases[k] += v
		}
		total.Models = append(total.Models, t.Models...)
		total.Tools = append(total.Tools, t.Tools...)
		total.Gaps = append(total.Gaps, t.Gaps...)
		total.Stages = append(total.Stages, t.Stages...)
		concurrent += t.Concurrent * float64(t.Wall)
	}
	if total.Wall > 0 {
		concurrent /= float64(total.Wall)
	}
	pct := func(d time.Duration) string {
		if total.Wall == 0 {
			return "0%"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(d)/float64(total.Wall))
	}
	fmt.Fprintf(&b, "# Timing\n\n%d trace(s), %s of wall clock. Model calls in flight while any is: %.2f on average. Agents spent %s blocked in wait_for_input.\n\n", len(ts), round(total.Wall), concurrent, round(total.Waiting))

	b.WriteString("## Where the wall clock went\n\nEach moment is counted once, by what was running: a model call, a tool, both, only the harness between an agent's steps, or nothing.\n\n| Bucket | Time | Share |\n| --- | ---: | ---: |\n")
	for _, k := range timingBuckets {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", k, round(total.Buckets[k]), pct(total.Buckets[k]))
	}

	phases := phaseOrder(total.Phases)
	b.WriteString("\n## Phases\n\nThe wall clock by the work being done at each moment: the kind of work item the active agent holds, or the manager coordinating. Moments where several ran at once are shared; harness and idle keep their names.\n\n| Phase | Time | Share | Per trace |\n| --- | ---: | ---: | ---: |\n")
	for _, ph := range phases {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", ph, round(total.Phases[ph]), pct(total.Phases[ph]), round(total.Phases[ph]/time.Duration(len(ts))))
	}

	b.WriteString("\n## Model calls by role\n\nWall is the role's share of the model bucket. Prefill is start to first token (queueing and prompt processing); reasoning runs to the first answer token.\n\n| Role | Calls | Wall | Share | p50 | p90 | Max | Prefill p50 | Prefill p90 | Reasoning | Answer | In tokens (mean) | Cached | Out tokens (mean) | Reasoning tokens | Out tok/s (p50) |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	byRole := map[string][]ModelCall{}
	for _, m := range total.Models {
		byRole[m.Role] = append(byRole[m.Role], m)
	}
	for _, role := range sortedKeys(byRole, func(r string) time.Duration { return total.ModelWall[r] }) {
		ms := byRole[role]
		var d, pre, rate []time.Duration
		var reasoning, answer time.Duration
		var in, out, cached, cachedIn, reasoningTokens, reasoningOut int64
		for _, m := range ms {
			if m.CachedTokens >= 0 {
				cached += m.CachedTokens
				cachedIn += m.InputTokens
			}
			if m.ReasoningTokens >= 0 {
				reasoningTokens += m.ReasoningTokens
				reasoningOut += m.OutputTokens
			}
			d, pre = append(d, m.Duration), append(pre, m.Prefill)
			reasoning += m.Reasoning
			answer += m.Answer
			in += m.InputTokens
			out += m.OutputTokens
			if gen := m.Duration - m.Prefill; gen > 0 && m.OutputTokens > 0 {
				rate = append(rate, time.Duration(float64(m.OutputTokens)/gen.Seconds()))
			}
		}
		n := int64(len(ms))
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %d | %s | %d | %s | %d |\n", orNone(role), n, round(total.ModelWall[role]), pct(total.ModelWall[role]), round(quantile(d, 0.5)), round(quantile(d, 0.9)), round(quantile(d, 1)), round(quantile(pre, 0.5)), round(quantile(pre, 0.9)), round(reasoning), round(answer), in/n, ratio(cached, cachedIn), out/n, ratio(reasoningTokens, reasoningOut), int64(quantile(rate, 0.5)))
	}

	b.WriteString("\n## Tools\n\nWall is the tool's share of the tool buckets; total is the summed call time, which counts overlapping calls once each. wait_for_input is reported above as blocked time.\n\n| Tool | Calls | Wall | Total | p50 | p90 | Max | Failed |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	byTool := map[string][]ToolCall{}
	for _, c := range total.Tools {
		byTool[c.Name] = append(byTool[c.Name], c)
	}
	for _, name := range sortedKeys(byTool, func(n string) time.Duration { return total.ToolWall[n] }) {
		var d []time.Duration
		var sum time.Duration
		failed := 0
		for _, c := range byTool[name] {
			d = append(d, c.Duration)
			sum += c.Duration
			if c.Failed {
				failed++
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %d |\n", name, len(d), round(total.ToolWall[name]), round(sum), round(quantile(d, 0.5)), round(quantile(d, 0.9)), round(quantile(d, 1)), failed)
	}

	b.WriteString("\n## Harness gaps\n\nTime an agent was running between its own steps: dispatching tools after a model call, or building the next request after tools.\n\n")
	b.WriteString(durationTable("Gap", func(add func(string, time.Duration)) {
		for _, g := range total.Gaps {
			add(g.Kind, g.Duration)
		}
	}))
	b.WriteString("\n## Protocol stages\n\n")
	b.WriteString(durationTable("Stage", func(add func(string, time.Duration)) {
		for _, s := range total.Stages {
			add(s.Name, s.Duration)
		}
	}))
	b.WriteString(serverMarkdown(ts))
	if len(ts) > 1 {
		b.WriteString("\n## Traces\n\n| Task | Wall | " + strings.Join(phases, " | ") + " | Model calls | Tool calls | Slowest tool |\n| --- | ---: |" + strings.Repeat(" ---: |", len(phases)) + " ---: | ---: | --- |\n")
		for _, t := range ts {
			slowest := ""
			var longest time.Duration
			for _, c := range t.Tools {
				if c.Duration > longest {
					longest, slowest = c.Duration, fmt.Sprintf("%s %s", c.Name, round(c.Duration))
				}
			}
			cells := make([]string, len(phases))
			for i, ph := range phases {
				cells[i] = round(t.Phases[ph])
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %s |\n", orNone(t.Task), round(t.Wall), strings.Join(cells, " | "), len(t.Models), len(t.Tools), slowest)
		}
	}
	return b.String()
}

// serverMarkdown reports the model server's metrics summed across traces.
func serverMarkdown(ts []Timing) string {
	d := map[string]float64{}
	peak := map[string]float64{}
	samples := 0
	for _, t := range ts {
		if t.Server == nil {
			continue
		}
		samples += t.Server.Samples
		for k, v := range t.Server.Deltas {
			d[k] += v
		}
		for k, v := range t.Server.Peak {
			peak[k] = max(peak[k], v)
		}
	}
	if samples == 0 {
		return ""
	}
	seconds := func(sum, count string) string {
		if d[count] == 0 {
			return "—"
		}
		return fmt.Sprintf("%s total, %s mean over %.0f requests", round(secondsOf(d[sum])), round(secondsOf(d[sum]/d[count])), d[count])
	}
	share := func(part, whole string) string {
		if d[whole] == 0 {
			return "—"
		}
		return fmt.Sprintf("%.1f%% (%.0f of %.0f)", 100*d[part]/d[whole], d[part], d[whole])
	}
	var b strings.Builder
	b.WriteString("\n## Model server\n\nFrom vLLM's /metrics at the start and end of each session and every few seconds between. Server-wide: exact only while the session was its sole client.\n\n| Measure | Value |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| Queue time (waiting to be scheduled) | %s |\n", seconds("vllm:request_queue_time_seconds_sum", "vllm:request_queue_time_seconds_count"))
	fmt.Fprintf(&b, "| Prefill time | %s |\n", seconds("vllm:request_prefill_time_seconds_sum", "vllm:request_prefill_time_seconds_count"))
	fmt.Fprintf(&b, "| Decode time | %s |\n", seconds("vllm:request_decode_time_seconds_sum", "vllm:request_decode_time_seconds_count"))
	fmt.Fprintf(&b, "| Time to first token | %s |\n", seconds("vllm:time_to_first_token_seconds_sum", "vllm:time_to_first_token_seconds_count"))
	fmt.Fprintf(&b, "| Prefix cache hits (tokens) | %s |\n", share("vllm:prefix_cache_hits_total", "vllm:prefix_cache_queries_total"))
	fmt.Fprintf(&b, "| Prompt tokens served from cache | %s |\n", share("vllm:prompt_tokens_cached_total", "vllm:prompt_tokens_total"))
	fmt.Fprintf(&b, "| Speculative tokens accepted | %s |\n", share("vllm:spec_decode_num_accepted_tokens_total", "vllm:spec_decode_num_draft_tokens_total"))
	if d["vllm:spec_decode_num_drafts_total"] > 0 {
		fmt.Fprintf(&b, "| Accepted tokens per draft | %.2f |\n", d["vllm:spec_decode_num_accepted_tokens_total"]/d["vllm:spec_decode_num_drafts_total"])
	}
	fmt.Fprintf(&b, "| Preemptions | %.0f |\n", d["vllm:num_preemptions_total"])
	fmt.Fprintf(&b, "| Peak requests running / waiting | %.0f / %.0f |\n", peak["vllm:num_requests_running"], peak["vllm:num_requests_waiting"])
	fmt.Fprintf(&b, "| Peak KV cache use | %.0f%% |\n", 100*peak["vllm:kv_cache_usage_perc"])
	return b.String()
}

func secondsOf(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// phaseOrder lists phases in protocol order, then any others by time.
func phaseOrder(m map[string]time.Duration) []string {
	known := []string{"manager", "review", "research", "experiment", "implementation", "audit", "repair", "debugger", "harness", "idle"}
	var out []string
	for _, k := range known {
		if m[k] > 0 {
			out = append(out, k)
		}
	}
	for _, k := range sortedKeys(m, func(k string) time.Duration { return m[k] }) {
		if !slices.Contains(known, k) {
			out = append(out, k)
		}
	}
	return out
}

func durationTable(label string, collect func(add func(string, time.Duration))) string {
	groups := map[string][]time.Duration{}
	collect(func(k string, d time.Duration) { groups[k] = append(groups[k], d) })
	if len(groups) == 0 {
		return "None recorded.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "| %s | Count | Total | p50 | p90 | Max |\n| --- | ---: | ---: | ---: | ---: | ---: |\n", label)
	sums := map[string]time.Duration{}
	for k, ds := range groups {
		for _, d := range ds {
			sums[k] += d
		}
	}
	for _, k := range sortedKeys(groups, func(k string) time.Duration { return sums[k] }) {
		ds := groups[k]
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s |\n", k, len(ds), round(sums[k]), round(quantile(ds, 0.5)), round(quantile(ds, 0.9)), round(quantile(ds, 1)))
	}
	return b.String()
}

// sortedKeys orders a map's keys by weight, largest first.
func sortedKeys[V any](m map[string]V, weight func(string) time.Duration) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if wi, wj := weight(keys[i]), weight(keys[j]); wi != wj {
			return wi > wj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// quantile returns the q-quantile (nearest rank) of ds; 1 is the maximum.
func quantile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(q*float64(len(s)) + 0.5)
	if i < 1 {
		i = 1
	}
	if i > len(s) {
		i = len(s)
	}
	return s[i-1]
}

func round(d time.Duration) string {
	switch {
	case d >= time.Minute:
		return d.Round(time.Second).String()
	case d >= time.Second:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}

// ratio is part/whole as a percentage, or a dash when the server reported
// no counts.
func ratio(part, whole int64) string {
	if whole <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(part)/float64(whole))
}

func orNone(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
