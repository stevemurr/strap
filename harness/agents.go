package harness

import (
	"context"
	"encoding/json"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
)

// Agent tools are selected per role. Tool contracts remain independent of the
// controller and expose only application callbacks.
type agentControl interface {
	StopAgent(message.ActorID) (conversation.AgentInfo, error)
	PauseAgent(message.ActorID) (conversation.AgentInfo, error)
	ResumeAgent(message.ActorID) (conversation.AgentInfo, error)
	InspectAgent(message.ActorID, conversation.InspectOptions) (AgentInspection, error)
	Agents() []AgentInfo
}

// topology is the graph the agent tools consult; nil means unrestricted, as
// for host tests that exercise the tools without a session.
type topology func() *roster.Graph

// agentReads show agents the caller holds a read edge to, and itself: the
// manager sees its workers, the debugger sees everyone, and nobody else reads
// another agent.
func agentReads(c agentControl, graph topology) []tool.Tool {
	inspect := inspectTool(c)
	return []tool.Tool{
		guarded{Tool: inspect, check: func(call tool.Call) error {
			var in struct {
				Input tool.InspectAgentArgs `json:"input"`
			}
			if err := json.Unmarshal(call.Arguments, &in); err != nil {
				return nil // The tool reports malformed input itself.
			}
			return reach(graph, call.Actor, in.Input.AgentID, roster.Read)
		}},
		tool.ListAgents(func(_ context.Context, call tool.Call) (tool.Result, error) {
			var out []AgentInfo
			for _, a := range c.Agents() {
				if reach(graph, call.Actor, a.ID, roster.Read) == nil {
					out = append(out, a)
				}
			}
			return agentJSON(out)
		}),
	}
}

// agentControls stop, pause and resume only the agents the caller holds a
// control edge to: the manager its workers, the debugger every worker. Nobody
// can stop, pause or resume the manager or the debugger, whose lifecycle
// belongs to the bootstrap. An agent that could stop the session's only
// manager did so once to "pause the current work", and the session could do
// nothing after that.
func agentControls(c agentControl, graph topology) []tool.Tool {
	owned := func(operation func(message.ActorID) (conversation.AgentInfo, error)) func(context.Context, tool.Call, message.ActorID) (tool.Result, error) {
		return func(ctx context.Context, call tool.Call, id message.ActorID) (tool.Result, error) {
			if err := ctx.Err(); err != nil {
				return tool.Result{}, err
			}
			if err := reach(graph, call.Actor, id, roster.Control); err != nil {
				return tool.Result{}, err
			}
			info, err := operation(id)
			if err != nil {
				return tool.Result{}, err
			}
			return agentJSON(info)
		}
	}
	return []tool.Tool{
		tool.StopAgent(owned(c.StopAgent)),
		tool.PauseAgent(owned(c.PauseAgent)),
		tool.ResumeAgent(owned(c.ResumeAgent)),
	}
}

// reach checks caller's kind edge to target. An agent may always read itself.
// A caller unknown to the tools, such as a host test, is not restricted.
func reach(graph topology, caller, target message.ActorID, kind roster.Edge) error {
	if caller == "" || graph == nil || graph() == nil || kind == roster.Read && caller == target {
		return nil
	}
	return graph().Check(caller, target, kind)
}

// guarded runs check before the tool it wraps.
type guarded struct {
	tool.Tool
	check func(tool.Call) error
}

func (g guarded) InputContract() tool.Contract {
	if typed, ok := g.Tool.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (g guarded) Validate() error { return tool.ValidateTool(g.Tool) }
func (g guarded) BookkeepingParameters() []string {
	if b, ok := g.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}
func (g guarded) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := g.check(call); err != nil {
		return tool.Result{}, err
	}
	return g.Tool.Call(ctx, call)
}
