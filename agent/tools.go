package agent

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// ToolActivity describes an actual dispatch, including unknown-tool attempts.
// FinishedAt is zero for the start notification. Each notification owns its
// arguments and result content; host observers cannot mutate agent history.
type ToolActivity struct {
	InvocationID string
	Diagnostic   *tool.Diagnostic
	Call         provider.ToolCall
	StartedAt    time.Time
	FinishedAt   time.Time
	Result       tool.Result
	Err          error
}

// ToolBatch identifies the history after all results from one model response
// have been appended. A complete batch is a valid boundary for tokenization.
type ToolBatch struct {
	Calls           []string
	ContextRevision uint64
}

func (a *Agent) reportTool(activity ToolActivity) {
	if a.config.OnTool == nil {
		return
	}
	activity.Call = provider.CopyCalls([]provider.ToolCall{activity.Call})[0]
	activity.Result.Content = activity.Result.Content.Clone()
	if activity.Diagnostic != nil {
		d := activity.Diagnostic.Clone()
		activity.Diagnostic = &d
	}
	a.config.OnTool(activity)
}

// toolActivityWire preserves malformed argument bytes as a string and error text
// as data. Provider call IDs are metadata, not unique invocation identities.
type toolActivityWire struct {
	InvocationID string           `json:"invocation_id"`
	CallID       string           `json:"call_id"`
	Name         string           `json:"name"`
	Arguments    string           `json:"arguments"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   time.Time        `json:"finished_at"`
	Result       tool.Result      `json:"result"`
	Error        string           `json:"error,omitempty"`
	Diagnostic   *tool.Diagnostic `json:"diagnostic,omitempty"`
}

func (a ToolActivity) MarshalJSON() ([]byte, error) {
	w := toolActivityWire{InvocationID: a.InvocationID, CallID: a.Call.ID, Name: a.Call.Name, Arguments: string(a.Call.Arguments), StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, Result: a.Result, Diagnostic: a.Diagnostic}
	if a.Err != nil {
		w.Error = a.Err.Error()
	}
	return json.Marshal(w)
}
func (a *ToolActivity) UnmarshalJSON(raw []byte) error {
	var w toolActivityWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	*a = ToolActivity{InvocationID: w.InvocationID, Call: provider.ToolCall{ID: w.CallID, Name: w.Name, Arguments: json.RawMessage(w.Arguments)}, StartedAt: w.StartedAt, FinishedAt: w.FinishedAt, Result: w.Result, Diagnostic: w.Diagnostic}
	if w.Error != "" {
		a.Err = errors.New(w.Error)
	}
	return nil
}
