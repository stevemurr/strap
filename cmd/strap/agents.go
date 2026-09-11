package main

import (
	"context"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/tool"
)

// Management is selected by the application for its root spec. Tool contracts
// remain independent of the controller and expose only application callbacks.
func managementTools(c *conversation.Controller) []tool.Tool {
	snapshot := func(operation func(message.ActorID) (conversation.AgentInfo, error)) func(context.Context, tool.Call, message.ActorID) (tool.Result, error) {
		return func(ctx context.Context, _ tool.Call, id message.ActorID) (tool.Result, error) {
			if err := ctx.Err(); err != nil {
				return tool.Result{}, err
			}
			info, err := operation(id)
			if err != nil {
				return tool.Result{}, err
			}
			return tool.JSON(info)
		}
	}
	return []tool.Tool{
		tool.StopAgent(snapshot(c.StopAgent)),
		tool.PauseAgent(snapshot(c.PauseAgent)),
		tool.ResumeAgent(snapshot(c.ResumeAgent)),
		inspectTool(c),
		tool.ListAgents(func(_ context.Context, _ tool.Call) (tool.Result, error) { return tool.JSON(c.Agents()) }),
	}
}
