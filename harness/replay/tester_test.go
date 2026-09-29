package replay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/replay"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// soloAgent changes a file, checks it and replies; held, it replies again.
type soloAgent struct{ calls atomic.Int32 }

func (p *soloAgent) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("a%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 1:
		return call("write_file", `{"input":{"path":"b.txt","content":"hello\n"}}`), nil
	case 2:
		return call("shell", `{"input":{"command":"cat b.txt","timeout_ms":null}}`), nil
	case 3:
		return provider.Response{Content: "Done."}, nil
	}
	return provider.Response{Content: "Fixed."}, nil
}

// adversary writes a failing check and reports it.
type adversary struct{ calls atomic.Int32 }

func (p *adversary) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("t%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 1:
		return call("write_file", `{"input":{"path":"check.sh","content":"grep -q HELLO b.txt\n"}}`), nil
	case 2:
		return call("report_failure", `{"input":{"path":"check.sh","command":"sh check.sh","requirement":"b.txt says HELLO"}}`), nil
	}
	return provider.Response{Content: "Covered b.txt."}, nil
}

// A solo session's tester run replays from its recording: the harness serves
// the recorded run instead of running the tester, so the agent sees the same
// failures and every request matches.
func TestSoloTesterRunReplaysFaithfully(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LSP, cfg.Solo, cfg.Tester = workspace, nil, nil, true, true
	cfg.Telemetry.ContextTokens = false
	cfg.Events.JSONLPath = filepath.Join(dir, "trace.jsonl")
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Agent: harness.AgentDependencies{Provider: &soloAgent{}}, Tester: harness.AgentDependencies{Provider: &adversary{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(s.Manager(), "Please write hello to b.txt."); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for replied := false; !replied; {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m, ok := e.(conversation.MessageEvent); ok && m.Message.To == message.User && m.Message.Content == "Fixed." {
			replied = true
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_ = s.Dispose(ctx)

	rec := open(t, cfg.Events.JSONLPath)
	if len(rec.TesterRuns) != 1 || len(rec.TesterRuns[0].Failures) != 1 || !rec.Config.Tester {
		t.Fatalf("tester %v, runs %+v", rec.Config.Tester, rec.TesterRuns)
	}
	again := filepath.Join(t.TempDir(), "replay.jsonl")
	res, err := replay.Run(ctx, rec, replay.Options{Trace: again, Stall: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Faithful() {
		t.Fatalf("served %d of %d, divergences:\n%s", res.Served, res.Recorded, divergences(res))
	}
	replayed := open(t, again)
	if len(replayed.TesterRuns) != 1 || !strings.Contains(replayed.TesterRuns[0].Failures[0].Requirement, "HELLO") {
		t.Fatalf("%+v", replayed.TesterRuns)
	}
}
