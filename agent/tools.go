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
	InvocationID string            `json:"invocation_id"`
	Diagnostic   *tool.Diagnostic  `json:"diagnostic"`
	Call         provider.ToolCall `json:"call"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	Result       tool.Result       `json:"result"`
	Err          error             `json:"err"`
}

// ToolBatch identifies the history after all results from one model response
// have been appended. A complete batch is a valid boundary for tokenization.
type ToolBatch struct {
	Calls           []string `json:"calls"`
	ContextRevision uint64   `json:"context_revision"`
}

func (a *Agent) reportTool(activity ToolActivity) error {
	activity.Call = provider.CopyCalls([]provider.ToolCall{activity.Call})[0]
	activity.Result.Content = activity.Result.Content.Clone()
	if activity.Diagnostic != nil {
		d := activity.Diagnostic.Clone()
		activity.Diagnostic = &d
	}
	if err := a.report(activity); err != nil {
		return err
	}
	if a.config.OnTool != nil {
		a.config.OnTool(activity)
	}
	return nil
}
