package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
)

// rootDies scripts a root that delegates and then fails on its next model
// call, and an implementor that keeps working with one-second shell sleeps.
type rootDies struct{ calls atomic.Int32 }

func (p *rootDies) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if r.Agent != "agent-1" {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "w", Name: "shell", Arguments: json.RawMessage(`{"command":"sleep 1"}`)}}}, nil
	}
	n := p.calls.Add(1)
	call := func(name, args string) provider.Response {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("c%d", n), Name: name, Arguments: json.RawMessage(args)}}}
	}
	switch n {
	case 1:
		return call("create_agent", `{"role":"implementor"}`), nil
	case 2:
		assignee := "agent-2"
		for i := len(r.Messages) - 1; i >= 0; i-- {
			if r.Messages[i].Role == "tool" {
				if m := regexp.MustCompile(`"agent_id":"([^"]+)"`).FindStringSubmatch(r.Messages[i].Content.Text()); m != nil {
					assignee = m[1]
				}
				break
			}
		}
		return call("assign_implementation", fmt.Sprintf(`{"assignee":%q,"task":"keep busy"}`, assignee)), nil
	default:
		return provider.Response{}, errors.New("model exploded")
	}
}

// Workers are stopped when the root exits with an error; nothing could
// receive their results, and in ladder runs they kept running and failing
// their sends until the session budget ended.
func TestWorkersStopWhenRootFails(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	cfg.Telemetry.ContextTokens = false
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: &rootDies{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err := s.Send(s.Root(), "go"); err != nil {
		t.Fatal(err)
	}
	awaitAgentState(t, s, s.Root(), agent.Failed)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var worker *agent.State
		for _, a := range s.Agents() {
			if a.ID != s.Root() {
				state := a.State
				worker = &state
			}
		}
		if worker != nil && *worker == agent.Stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker still %v after the root failed", worker)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
