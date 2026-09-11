package agent

import (
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// ToolActivity describes an actual dispatch, including unknown-tool attempts.
// FinishedAt is zero for the start notification. Each notification owns its
// arguments and result content; host observers cannot mutate agent history.
type ToolActivity struct {
	Call       provider.ToolCall
	StartedAt  time.Time
	FinishedAt time.Time
	Result     tool.Result
	Err        error
}

func (a *Agent) reportTool(activity ToolActivity) {
	if a.config.OnTool == nil {
		return
	}
	activity.Call = provider.CopyCalls([]provider.ToolCall{activity.Call})[0]
	activity.Result.Content = activity.Result.Content.Clone()
	a.config.OnTool(activity)
}
