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
	for _, operation := range managementTools(c) {
		kit[operation.Definition().Name] = operation
	}
	if len(kit) != 5 {
		t.Fatal("incomplete management tool set")
	}
	args, _ := json.Marshal(map[string]any{"agent_id": child.AgentID})
	invoke := func(name string) conversation.AgentInfo {
		t.Helper()
		result, err := kit[name].Call(ctx, tool.Call{Actor: root.AgentID, Arguments: args})
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
	if _, err := kit["inspect_agent"].Call(ctx, tool.Call{Arguments: json.RawMessage(`{"agent_id":"missing"}`)}); err == nil {
		t.Fatal("unknown target accepted")
	}
	result, err := kit["list_agents"].Call(ctx, tool.Call{Arguments: json.RawMessage(`{}`)})
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
	for _, op := range managementTools(c) {
		if op.Definition().Name == "list_agents" {
			continue
		}
		if _, err := op.Call(context.Background(), tool.Call{Arguments: json.RawMessage(`{"agent_id":"missing"}`)}); err == nil {
			t.Fatal("unknown agent accepted")
		}
	}
}
func TestInspectionPagingOptionsReachController(t *testing.T) {
	c := conversation.New(context.Background())
	defer c.Close(context.Background())
	if _, err := inspectTool(c).Call(context.Background(), tool.Call{Arguments: json.RawMessage(`{"agent_id":"missing","limit":3,"before":2}`)}); err == nil {
		t.Fatal("unknown agent accepted")
	}
}
