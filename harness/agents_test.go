package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
)

type unusedProvider struct{}

func (unusedProvider) Submit(ctx context.Context, _ provider.Request, observer provider.Observer) (provider.Response, error) {
	<-ctx.Done()
	return provider.Response{}, ctx.Err()
}
func TestApplicationManagementTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := conversation.New(ctx)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := c.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	spec := agent.Spec{Provider: unusedProvider{}}
	root, err := c.CreateAgent(message.User, spec)
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateAgent(root.AgentID, spec)
	if err != nil {
		t.Fatal(err)
	}
	kit := map[string]tool.Tool{}
	for _, operation := range append(agentControls(runtimeFixture{c}, nil), agentReads(runtimeFixture{c}, nil)...) {
		kit[operation.Definition().Name] = operation
	}
	if len(kit) != 5 {
		t.Fatal("incomplete management tool set")
	}
	args, _ := tool.MarshalInput(map[string]any{"agent_id": child.AgentID})
	invoke := func(name string) conversation.AgentInfo {
		t.Helper()
		input := args
		if name == "inspect_agent" {
			input, _ = tool.MarshalInput(tool.InspectAgentArgs{AgentID: child.AgentID})
		}
		result, err := kit[name].Call(ctx, tool.Call{Actor: root.AgentID, Arguments: input})
		if err != nil {
			t.Fatal(err)
		}
		var info conversation.AgentInfo
		if err := json.Unmarshal([]byte(result.Content.Text()), &info); err != nil {
			t.Fatal(err)
		}
		if info.ID != child.AgentID || info.Parent != root.AgentID {
			t.Fatal("wrong management target")
		}
		return info
	}
	if info := invoke("pause_agent"); info.State != agent.PauseRequested {
		t.Fatal(info)
	}
	for {
		e, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := e.(conversation.AgentStateChanged); ok && s.Agent == child.AgentID && s.State == agent.Paused {
			break
		}
	}
	if info := invoke("inspect_agent"); info.State != agent.Paused {
		t.Fatal(info)
	}
	if info := invoke("resume_agent"); info.State != agent.Running {
		t.Fatal(info)
	}
	if info := invoke("stop_agent"); info.State != agent.StopRequested && !info.State.Terminal() {
		t.Fatal(info)
	}
	if _, err := kit["inspect_agent"].Call(ctx, tool.Call{Arguments: json.RawMessage(`{"input":{"agent_id":"missing","before":null,"limit":null}}`)}); err == nil {
		t.Fatal("unknown target accepted")
	}
	result, err := kit["list_agents"].Call(ctx, tool.Call{Arguments: json.RawMessage(`{"input":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	var agents []conversation.AgentInfo
	if err := json.Unmarshal([]byte(result.Content.Text()), &agents); err != nil || len(agents) != 2 {
		t.Fatalf("list: %+v %v", agents, err)
	}
}

func TestUnknownAgentErrors(t *testing.T) {
	c := conversation.New(context.Background())
	defer c.Close(context.Background())
	for _, op := range append(agentControls(runtimeFixture{c}, nil), agentReads(runtimeFixture{c}, nil)...) {
		if op.Definition().Name == "list_agents" {
			continue
		}
		if _, err := op.Call(context.Background(), tool.Call{Arguments: json.RawMessage(`{"input":{"agent_id":"missing","before":null,"limit":null}}`)}); err == nil {
			t.Fatal("unknown agent accepted")
		}
	}
}

type runtimeFixture struct{ *conversation.Controller }

func (f runtimeFixture) Agents() []AgentInfo {
	out := []AgentInfo{}
	for _, a := range f.Controller.Agents() {
		out = append(out, AgentInfo{AgentInfo: a})
	}
	return out
}
func (f runtimeFixture) InspectAgent(id message.ActorID, o conversation.InspectOptions) (AgentInspection, error) {
	a, e := f.Controller.InspectAgent(id, o)
	return AgentInspection{AgentInspection: a}, e
}

// Lifecycle control and reads follow the topology's edges: the manager runs
// its own workers and nothing else, and nobody can stop, pause or resume the
// manager, whose lifecycle belongs to the bootstrap.
func TestAgentToolsFollowTheTopology(t *testing.T) {
	g := roster.NewGraph()
	for _, r := range []roster.Registration{
		{AgentID: "agent-2", Parent: roster.User, Role: roster.Manager},
		{AgentID: "agent-3", Parent: "agent-2", Role: roster.Implementor},
		{AgentID: "agent-4", Parent: "agent-2", Role: roster.Implementor},
	} {
		if err := g.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	graph := func() *roster.Graph { return g }
	for _, c := range []struct {
		caller, target message.ActorID
		kind           roster.Edge
		allowed        bool
	}{
		{"agent-2", "agent-3", roster.Control, true},  // The manager runs its worker.
		{"agent-2", "agent-2", roster.Control, false}, // Not itself.
		{"agent-3", "agent-2", roster.Control, false}, // Workers control nothing,
		{"agent-3", "agent-4", roster.Control, false}, // not even each other.
		{"agent-2", "agent-3", roster.Read, true},
		{"agent-3", "agent-2", roster.Read, false}, // A worker reads only itself.
		{"agent-3", "agent-3", roster.Read, true},  // Everyone may read itself.
	} {
		if err := reach(graph, c.caller, c.target, c.kind); (err == nil) != c.allowed {
			t.Errorf("%s -%s-> %s: allowed=%v, err=%v", c.caller, c.kind, c.target, c.allowed, err)
		}
	}
}
