package replay_test

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/machine"
	"github.com/stevemurr/strap/harness/replay"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// scripted plays a user agent that forwards to the manager and relays its
// reply; a manager that plans, staffs an implementor, assigns it the step,
// waits, cancels and reports; and an implementor that reads a file, writes
// one and runs the shell. Ledger ids, all three environment kinds and workspace
// changes all appear in one short session.
type scripted struct {
	mu    sync.Mutex
	calls map[message.ActorID]int
}

func call(name, args string) (provider.Response, error) {
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: "call-" + name, Name: name, Arguments: []byte(args)}}}, nil
}

// latest finds the last match of pattern in the agent's tool results.
func latest(r provider.Request, pattern string) string {
	re := regexp.MustCompile(pattern)
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if m := re.FindStringSubmatch(r.Messages[i].Content.Text()); r.Messages[i].Role == "tool" && m != nil {
			return m[1]
		}
	}
	return ""
}

func (p *scripted) Submit(_ context.Context, r provider.Request, o provider.Observer) (provider.Response, error) {
	p.mu.Lock()
	p.calls[r.Agent]++
	n := p.calls[r.Agent]
	p.mu.Unlock()
	switch r.Agent {
	case "agent-1": // The manager.
		switch n {
		case 1:
			return call("create_plan", `{"input":{"title":"Write b.txt","steps":[{"title":"Write b.txt","acceptance_criteria":null}]}}`)
		case 2:
			return call("create_agent", `{"input":{"role":"implementor"}}`)
		case 3:
			return call("assign_task", fmt.Sprintf(`{"input":{"kind":"implementation","assignee":%q,"task":"Write b.txt","context":null,"expected_output":null,"scope":{"plan_id":%q,"step_ids":[%q]}}}`,
				latest(r, `"agent_id":"([^"]+)"`), latest(r, `"plan_id":"([^"]+)"`), latest(r, `"step_id":"([^"]+)"`)))
		case 4:
			return call("wait_for_input", `{"input":{}}`)
		case 5:
			return call("cancel_work", fmt.Sprintf(`{"input":{"work_id":%q,"expected_revision":%s,"reason":"The test ends here."}}`,
				latest(r, `"work_id":"(work-[^"]+)"`), latest(r, `"work_id":"work-[^"]+".*?"revision":(\d+)`)))
		}
		return provider.Response{Reasoning: "All steps ran.", Content: "Done: b.txt holds hello."}, nil
	}
	switch n { // The implementor.
	case 1:
		return call("read_file", `{"input":{"path":"a.txt","limit":null,"offset":null}}`)
	case 2:
		return call("write_file", `{"input":{"path":"b.txt","content":"hello\n"}}`)
	case 3:
		return call("shell", `{"input":{"command":"cat b.txt && echo hi > c.txt","timeout_ms":null}}`)
	}
	return provider.Response{Content: "Wrote b.txt and c.txt."}, nil
}

// record runs the scripted session to a trace and waits until it settles.
func record(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LSP = workspace, nil, nil
	cfg.Telemetry.ContextTokens = false
	cfg.Events.JSONLPath = filepath.Join(dir, "trace.jsonl")
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: &scripted{calls: map[message.ActorID]int{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(s.Manager(), "Please write hello to b.txt."); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		relayed := false
		if tl, err := machine.Load(context.Background(), cfg.Events.JSONLPath, nil); err == nil {
			for _, m := range tl.RepliesToUser(0) {
				relayed = relayed || strings.Contains(m.Content, "hello")
			}
		}
		idle := true
		for _, a := range s.Agents() {
			idle = idle && a.State != agent.Running
		}
		if relayed && idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the scripted session never relayed the manager's report")
		}
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_ = s.Dispose(ctx)
	return cfg.Events.JSONLPath
}

func open(t *testing.T, path string) *replay.Recording {
	t.Helper()
	rec, err := replay.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// A recorded session replays with every request identical to the recorded
// one, and the replay's own trace replays the same way.
func TestRecordedSessionReplaysFaithfully(t *testing.T) {
	path := record(t)
	rec := open(t, path)
	if len(rec.Seed) == 0 || len(rec.Environment) != 3 || len(rec.Outputs) < 12 {
		t.Fatalf("seed %x, %d environment records, %d outputs", rec.Seed, len(rec.Environment), len(rec.Outputs))
	}
	// Named writes and shell writes are both recorded as workspace changes.
	changes := map[string][]string{}
	for _, e := range rec.Environment {
		changes[e.Name] = e.Changed
	}
	if !slices.Equal(changes["write_file"], []string{"b.txt"}) || !slices.Equal(changes["shell"], []string{"c.txt"}) || len(changes["read_file"]) != 0 {
		t.Fatalf("recorded changes %v", changes)
	}
	again := filepath.Join(t.TempDir(), "replay.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := replay.Run(ctx, rec, replay.Options{Trace: again, Stall: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Faithful() {
		t.Fatalf("served %d of %d, divergences:\n%s", res.Served, res.Recorded, divergences(res))
	}
	t.Logf("served %d of %d outputs, %d environment calls, seed %x…", res.Served, res.Recorded, len(rec.Environment), rec.Seed[:4])
	// The replay recorded itself: the same machine, and replayable again.
	want, _ := machine.Load(ctx, path, []machine.Intent{machine.Task})
	got, _ := machine.Load(ctx, again, []machine.Intent{machine.Task})
	t.Logf("paths:\n%s", got.Paths())
	if w, g := want.Paths(), got.Paths(); w != g {
		t.Fatalf("paths differ:\nrecorded:\n%s\nreplayed:\n%s", w, g)
	}
	if replayed := open(t, again); replayed.Session != rec.Session {
		t.Fatalf("replay ran as session %s, recorded %s", replayed.Session, rec.Session)
	}
	for id, e := range open(t, again).Environment {
		if !slices.Equal(e.Changed, rec.Environment[id].Changed) {
			t.Fatalf("replayed %s changed %v, recorded %v", id, e.Changed, rec.Environment[id].Changed)
		}
	}
	res, err = replay.Run(ctx, open(t, again), replay.Options{Stall: 2 * time.Second})
	if err != nil || !res.Faithful() {
		t.Fatalf("replay of the replay: %v\n%s", err, divergences(res))
	}
}

// A changed model output shows up at the first request it reaches.
func TestReplayReportsWhereARunDeparts(t *testing.T) {
	rec := open(t, record(t))
	changed := false
	for i, o := range rec.Outputs {
		if o.Agent == "agent-2" && o.Response != nil && len(o.Response.ToolCalls) == 1 && o.Response.ToolCalls[0].Name == "read_file" {
			m := *o.Response
			m.ToolCalls = []provider.ToolCall{{ID: m.ToolCalls[0].ID, Name: "read_file", Arguments: []byte(`{"input":{"path":"missing.txt","limit":null,"offset":null}}`)}}
			rec.Outputs[i].Response = &m
			changed = true
		}
	}
	if !changed {
		t.Fatal("no read_file output to change")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := replay.Run(ctx, rec, replay.Options{Stall: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	report := divergences(res)
	t.Logf("divergences:\n%s", report)
	if !strings.Contains(report, "agent-2 environment: agent-2/tool-1 ran read_file") || !strings.Contains(report, "agent-2 call 2 request: message") {
		t.Fatalf("divergences:\n%s", report)
	}
}

func divergences(r replay.Result) string {
	var lines []string
	for _, d := range r.Divergences {
		lines = append(lines, d.String())
	}
	return strings.Join(lines, "\n")
}

// Every recorded run kept in testdata, live or from the ladder, replays with
// each of its model requests identical on this machine. That needs the seeded
// ids, the environment records, the intake, the send order and the recorded
// machine facts all to hold. Traces are kept only while the current harness
// reproduces them: after a change to prompts or tools, record fresh ones.
func TestRecordingsReplayFaithfully(t *testing.T) {
	traces, _ := filepath.Glob("testdata/*.jsonl.gz")
	if len(traces) == 0 {
		t.Skip("no recordings in testdata")
	}
	for _, trace := range traces {
		t.Run(filepath.Base(trace), func(t *testing.T) {
			in, err := os.Open(trace)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			z, err := gzip.NewReader(in)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "trace.jsonl")
			out, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(out, z); err != nil {
				t.Fatal(err)
			}
			out.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			res, err := replay.Run(ctx, open(t, path), replay.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Faithful() {
				t.Fatalf("served %d of %d, divergences:\n%s", res.Served, res.Recorded, divergences(res))
			}
		})
	}
}
