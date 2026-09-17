package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// TaskMetrics summarizes one attempt from its result and recorded trace.
type TaskMetrics struct {
	TaskID           string         `json:"task_id"`
	Tier             string         `json:"tier"`
	Title            string         `json:"title"`
	Outcome          Outcome        `json:"outcome"`
	Passed           bool           `json:"passed"`
	TimedOut         bool           `json:"timed_out"`
	NoReply          bool           `json:"no_reply"`
	Duration         time.Duration  `json:"duration_ns"`
	Events           int            `json:"events"`
	Agents           int            `json:"agents"`
	Roles            map[string]int `json:"roles"`
	ModelCalls       int            `json:"model_calls"`
	InputTokens      int64          `json:"input_tokens"`
	OutputTokens     int64          `json:"output_tokens"`
	MaxContextTokens int64          `json:"max_context_tokens"`
	ToolCalls        map[string]int `json:"tool_calls"`
	ToolErrors       map[string]int `json:"tool_errors"`
	ToolTotal        int            `json:"tool_total"`
	ToolErrorTotal   int            `json:"tool_error_total"`
	ReasoningBytes   uint64         `json:"reasoning_bytes"`
	LongestCall      time.Duration  `json:"longest_call_ns"`
	CanceledOutputs  int            `json:"canceled_outputs"`
	Messages         int            `json:"messages"`
	Replies          int            `json:"replies"`
	TimeToFirstReply time.Duration  `json:"time_to_first_reply_ns"`
	Work             map[string]int `json:"work"`
	Audits           map[string]int `json:"audits"`
	OutputFailures   int            `json:"output_failures"`
	AgentErrors      int            `json:"agent_errors"`
	Diagnostics      map[string]int `json:"diagnostics"`
	ExecutionError   string         `json:"execution_error,omitempty"`
	CaptureError     string         `json:"capture_error,omitempty"`
	GradeTail        string         `json:"grade_tail,omitempty"`
	Error            string         `json:"error,omitempty"`
}

type TierSummary struct {
	Tier            string        `json:"tier"`
	Tasks           int           `json:"tasks"`
	Passed          int           `json:"passed"`
	Failed          int           `json:"failed"`
	BuildFailed     int           `json:"build_failed"`
	Errored         int           `json:"errored"`
	TimedOut        int           `json:"timed_out"`
	NoReply         int           `json:"no_reply"`
	PassRate        float64       `json:"pass_rate"`
	MeanDuration    time.Duration `json:"mean_duration_ns"`
	MeanModelCalls  float64       `json:"mean_model_calls"`
	MeanToolCalls   float64       `json:"mean_tool_calls"`
	MeanToolErrors  float64       `json:"mean_tool_errors"`
	MeanAgents      float64       `json:"mean_agents"`
	MeanInputTokens float64       `json:"mean_input_tokens"`
	MeanOutput      float64       `json:"mean_output_tokens"`
}

type Report struct {
	Dir   string        `json:"dir"`
	Run   RunInfo       `json:"run"`
	Tiers []TierSummary `json:"tiers"`
	Tasks []TaskMetrics `json:"tasks"`
}

// Analyze reads results.jsonl and every trace under a run directory.
func Analyze(ctx context.Context, dir string) (Report, error) {
	rep := Report{Dir: dir}
	if data, err := os.ReadFile(filepath.Join(dir, "run.json")); err == nil {
		_ = json.Unmarshal(data, &rep.Run)
	}
	data, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return rep, err
	}
	latest := map[string]Result{}
	var order []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return rep, fmt.Errorf("results.jsonl: %w", err)
		}
		if _, seen := latest[r.TaskID]; !seen {
			order = append(order, r.TaskID)
		}
		latest[r.TaskID] = r
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := latest[order[i]], latest[order[j]]
		if tierOrder[a.Tier] != tierOrder[b.Tier] {
			return tierOrder[a.Tier] < tierOrder[b.Tier]
		}
		return a.TaskID < b.TaskID
	})
	for _, id := range order {
		m := analyzeTask(ctx, dir, latest[id])
		rep.Tasks = append(rep.Tasks, m)
	}
	rep.Tiers = summarize(rep.Tasks)
	return rep, nil
}

// analyzeTask reads the trace beside result.json under the run directory, so
// a run that has been moved or bundled still reports; the recorded path is
// only a fallback.
func analyzeTask(ctx context.Context, dir string, r Result) TaskMetrics {
	m := TaskMetrics{TaskID: r.TaskID, Tier: r.Tier, Title: r.Title, Outcome: r.Outcome, Passed: r.Passed, TimedOut: r.TimedOut, NoReply: r.NoReply, Duration: r.Duration, ExecutionError: r.ExecutionError,
		Roles: map[string]int{}, ToolCalls: map[string]int{}, ToolErrors: map[string]int{}, Work: map[string]int{}, Audits: map[string]int{}, Diagnostics: map[string]int{}}
	if r.Capture != nil {
		m.CaptureError = r.Capture.CaptureError
	}
	if r.Grade != nil && !r.Grade.Passed {
		m.GradeTail = failureLines(r.Grade.Output, 8)
	}
	if r.Error != "" && m.ExecutionError == "" {
		m.ExecutionError = r.Error
	}
	trace := filepath.Join(dir, r.TaskID, "trace.jsonl")
	if _, err := os.Stat(trace); err != nil {
		trace = r.Trace
	}
	if err := scanTrace(ctx, trace, &m); err != nil {
		m.Error = err.Error()
	}
	return m
}

func scanTrace(ctx context.Context, path string, m *TaskMetrics) error {
	src, err := eventlog.OpenJSONL(ctx, path)
	if err != nil {
		return err
	}
	defer src.Close(context.Background())
	reader, err := inspection.New(ctx, src)
	if err != nil {
		return err
	}
	defer reader.Close(context.Background())
	var root identity.ActorID
	var first, last, firstReply time.Time
	usage := 0
	outputs := 0
	starts := map[identity.OutputID]time.Time{}
	var after uint64
	for {
		page, err := src.Read(ctx, eventlog.Query{After: after, Limit: 1000})
		if err != nil {
			return err
		}
		if len(page.Events) == 0 {
			break
		}
		for _, e := range page.Events {
			after = e.Sequence
			m.Events++
			if first.IsZero() {
				first = e.Time
			}
			last = e.Time
			if !reportedKinds[e.Kind] {
				continue
			}
			if _, framed := record.Frame(e.Payload); framed {
				if e, err = reader.ResolveRecord(ctx, e); err != nil {
					return fmt.Errorf("resolve event %d: %w", e.Sequence, err)
				}
			}
			v, err := eventcodec.DecodeEvent(e)
			if err != nil || v == nil {
				continue
			}
			switch v := v.(type) {
			case conversation.AgentStarted:
				m.Agents++
				if v.Agent.Parent == message.User && root == "" {
					root = v.Agent.ID
				}
			case conversation.AgentRegistered:
				m.Roles[string(v.Registration.Role)]++
			case conversation.AgentExited:
				// A cancelled exit is a shutdown that outran its settle budget,
				// not something the task did wrong. A clean close reports no
				// error at all, so anything else here is a real failure.
				if v.Err != nil && !errors.Is(v.Err, context.Canceled) {
					m.AgentErrors++
				}
			case conversation.UsageEvent:
				usage++
				if u := v.Observation.Usage; u != nil {
					if u.InputTokens != nil {
						m.InputTokens += *u.InputTokens
					}
					if u.OutputTokens != nil {
						m.OutputTokens += *u.OutputTokens
					}
				}
			case conversation.AgentEvent:
				switch f := v.Event.(type) {
				case agent.OutputStarted:
					starts[f.Output] = f.StartedAt
				case agent.OutputFinished:
					outputs++
					m.ReasoningBytes += f.ReasoningBytes
					if f.Status != agent.OutputComplete {
						m.OutputFailures++
					}
					if f.Status == agent.OutputCanceled {
						m.CanceledOutputs++
					}
					if started, ok := starts[f.Output]; ok && !f.FinishedAt.IsZero() {
						m.LongestCall = max(m.LongestCall, f.FinishedAt.Sub(started))
					}
				}
			case conversation.ContextTokensEvent:
				m.MaxContextTokens = max(m.MaxContextTokens, v.Count)
			case conversation.ToolEvent:
				if v.Activity.FinishedAt.IsZero() {
					continue
				}
				m.ToolCalls[v.Activity.Call.Name]++
				m.ToolTotal++
				if v.Activity.Err != nil {
					m.ToolErrors[v.Activity.Call.Name]++
					m.ToolErrorTotal++
				}
			case conversation.MessageEvent:
				m.Messages++
				msg := v.Message
				if msg.From == root && msg.To == message.User && (msg.Kind == message.Reply || msg.Kind == message.Failure) {
					m.Replies++
					if firstReply.IsZero() {
						firstReply = e.Time
					}
				}
			case conversation.WorkEvent:
				m.Work[string(v.Event.Kind)]++
				if v.Event.Kind == work.AuditCompleted {
					m.Audits[string(v.Event.Work.State)]++
				}
			case conversation.DiagnosticEvent:
				m.Diagnostics[v.Level]++
			}
		}
	}
	m.ModelCalls = usage
	if usage == 0 {
		m.ModelCalls = outputs
	}
	if !first.IsZero() && m.Duration == 0 {
		m.Duration = last.Sub(first)
	}
	if !firstReply.IsZero() {
		m.TimeToFirstReply = firstReply.Sub(first)
	}
	return nil
}

var reportedKinds = map[string]bool{"output_started": true, "agent_started": true, "agent_registered": true, "agent_exited": true, "usage": true, "output_finished": true, "context_tokens": true, "tool": true, "message": true, "work": true, "diagnostic": true}

func summarize(tasks []TaskMetrics) []TierSummary {
	byTier := map[string]*TierSummary{}
	for _, t := range tasks {
		s := byTier[t.Tier]
		if s == nil {
			s = &TierSummary{Tier: t.Tier}
			byTier[t.Tier] = s
		}
		s.Tasks++
		switch t.Outcome {
		case Passed:
			s.Passed++
		case Failed:
			s.Failed++
		case BuildFailed:
			s.BuildFailed++
		default:
			s.Errored++
		}
		if t.TimedOut {
			s.TimedOut++
		}
		if t.NoReply {
			s.NoReply++
		}
		s.MeanDuration += t.Duration
		s.MeanModelCalls += float64(t.ModelCalls)
		s.MeanToolCalls += float64(t.ToolTotal)
		s.MeanToolErrors += float64(t.ToolErrorTotal)
		s.MeanAgents += float64(t.Agents)
		s.MeanInputTokens += float64(t.InputTokens)
		s.MeanOutput += float64(t.OutputTokens)
	}
	var out []TierSummary
	for _, tier := range Tiers() {
		s := byTier[tier]
		if s == nil {
			continue
		}
		n := float64(s.Tasks)
		s.PassRate = float64(s.Passed) / n
		s.MeanDuration = time.Duration(float64(s.MeanDuration) / n)
		s.MeanModelCalls /= n
		s.MeanToolCalls /= n
		s.MeanToolErrors /= n
		s.MeanAgents /= n
		s.MeanInputTokens /= n
		s.MeanOutput /= n
		out = append(out, *s)
	}
	return out
}

// Markdown renders the report for people; report.json holds the same data.
func (r Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# strap eval: %s\n\n", r.Dir)
	if r.Run.Model.Model != "" {
		fmt.Fprintf(&b, "Model %s at %s (backend %s). Started %s, parallel %d.", r.Run.Model.Model, r.Run.Model.BaseURL, r.Run.Model.Backend, r.Run.StartedAt.Format(time.RFC3339), r.Run.Parallel)
		if r.Run.Commit != "" {
			fmt.Fprintf(&b, " Harness commit %s.", r.Run.Commit)
		}
		if r.Run.Profile != "" {
			fmt.Fprintf(&b, " Profile %s.", r.Run.Profile)
		}
		b.WriteString("\n\n")
	}
	b.WriteString("## Tiers\n\n| tier | tasks | passed | rate | failed | build failed | error | timed out | no reply | mean time | calls | tools | tool errs | agents | tokens in | tokens out |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	total := TierSummary{Tier: "all"}
	for _, s := range r.Tiers {
		fmt.Fprintf(&b, "| %s | %d | %d | %.0f%% | %d | %d | %d | %d | %d | %s | %.1f | %.1f | %.1f | %.1f | %.0f | %.0f |\n", s.Tier, s.Tasks, s.Passed, 100*s.PassRate, s.Failed, s.BuildFailed, s.Errored, s.TimedOut, s.NoReply, s.MeanDuration.Round(time.Second), s.MeanModelCalls, s.MeanToolCalls, s.MeanToolErrors, s.MeanAgents, s.MeanInputTokens, s.MeanOutput)
		total.Tasks += s.Tasks
		total.Passed += s.Passed
	}
	if total.Tasks > 0 {
		fmt.Fprintf(&b, "\n%d of %d tasks passed (%.0f%%).\n", total.Passed, total.Tasks, 100*float64(total.Passed)/float64(total.Tasks))
	}
	b.WriteString("\n## Tasks\n\n| task | outcome | time | first reply | calls | longest call | reasoning KB | failed outputs | ctx max | tools (errors) | shell | agents | roles | work events | audits | replies | tokens in/out |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range r.Tasks {
		outcome := string(t.Outcome)
		if t.TimedOut {
			outcome += " (timed out)"
		}
		if t.NoReply {
			outcome += " (no reply)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s | %d | %d | %d | %d (%d) | %d | %d | %s | %d | %s | %d | %d/%d |\n", t.TaskID, outcome, t.Duration.Round(time.Second), t.TimeToFirstReply.Round(time.Second), t.ModelCalls, t.LongestCall.Round(time.Second), t.ReasoningBytes/1024, t.OutputFailures, t.MaxContextTokens, t.ToolTotal, t.ToolErrorTotal, t.ToolCalls["shell"], t.Agents, countList(t.Roles), sumMap(t.Work), countList(t.Audits), t.Replies, t.InputTokens, t.OutputTokens)
	}
	var failures []TaskMetrics
	for _, t := range r.Tasks {
		if !t.Passed || t.Error != "" {
			failures = append(failures, t)
		}
	}
	if len(failures) > 0 {
		b.WriteString("\n## Failures\n\n")
		for _, t := range failures {
			fmt.Fprintf(&b, "- **%s** (%s): %s", t.TaskID, t.Tier, t.Outcome)
			if t.TimedOut {
				b.WriteString(", session budget exhausted")
			}
			if t.NoReply {
				b.WriteString(", finished idle without a root reply")
			}
			if t.ExecutionError != "" {
				fmt.Fprintf(&b, "; execution: %s", oneLine(t.ExecutionError))
			}
			if t.CaptureError != "" {
				fmt.Fprintf(&b, "; capture: %s", oneLine(t.CaptureError))
			}
			if t.Error != "" {
				fmt.Fprintf(&b, "; analysis: %s", oneLine(t.Error))
			}
			if t.GradeTail != "" {
				fmt.Fprintf(&b, "\n\n  ```\n%s\n  ```", indent(t.GradeTail, "  "))
			}
			b.WriteString("\n")
		}
	}
	tools := map[string]int{}
	toolErrors := map[string]int{}
	for _, t := range r.Tasks {
		for k, v := range t.ToolCalls {
			tools[k] += v
		}
		for k, v := range t.ToolErrors {
			toolErrors[k] += v
		}
	}
	if len(tools) > 0 {
		b.WriteString("\n## Tool usage\n\n| tool | calls | errors |\n|---|---|---|\n")
		for _, k := range sortedKeys(tools) {
			fmt.Fprintf(&b, "| %s | %d | %d |\n", k, tools[k], toolErrors[k])
		}
	}
	return b.String()
}

func countList(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func sumMap(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// failureLines keeps the lines of go test output that explain a failure:
// test verdicts, panics, compiler messages and t.Fatal locations, but not the
// goroutine dump.
func failureLines(s string, limit int) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "--- FAIL"), strings.HasPrefix(trimmed, "panic:"), strings.HasPrefix(trimmed, "FAIL"), strings.HasPrefix(line, "# "), strings.HasPrefix(trimmed, "ok "):
		case strings.Contains(trimmed, ".go:") && !strings.Contains(trimmed, "/testing/") && !strings.Contains(trimmed, "/runtime/") && !strings.HasPrefix(trimmed, "/"):
		default:
			continue
		}
		if len(trimmed) > 200 {
			trimmed = trimmed[:200] + "…"
		}
		kept = append(kept, trimmed)
		if len(kept) == limit {
			break
		}
	}
	return strings.Join(kept, "\n")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// WriteReport stores report.md and report.json in the run directory.
func WriteReport(r Report) error {
	if err := os.WriteFile(filepath.Join(r.Dir, "report.md"), []byte(r.Markdown()), 0o644); err != nil {
		return err
	}
	return errors.Join(writeJSON(filepath.Join(r.Dir, "report.json"), r))
}
