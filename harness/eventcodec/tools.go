package eventcodec

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// Arguments are strings so malformed model JSON remains useful evidence. Runtime
// invocation IDs distinguish repeated provider call IDs within an agent.
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
type toolRecord struct {
	Agent    message.ActorID  `json:"agent"`
	Activity toolActivityWire `json:"activity"`
}

func encodeTool(a agent.ToolActivity) toolActivityWire {
	w := toolActivityWire{InvocationID: a.InvocationID, CallID: a.Call.ID, Name: a.Call.Name, Arguments: string(a.Call.Arguments), StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, Result: a.Result, Diagnostic: a.Diagnostic}
	if a.Err != nil {
		w.Error = a.Err.Error()
	}
	return w
}
func decodeTool(raw []byte) (conversation.Event, error) {
	var record toolRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	w := record.Activity
	a := agent.ToolActivity{InvocationID: w.InvocationID, Call: provider.ToolCall{ID: w.CallID, Name: w.Name, Arguments: json.RawMessage(w.Arguments)}, StartedAt: w.StartedAt, FinishedAt: w.FinishedAt, Result: w.Result, Diagnostic: w.Diagnostic}
	if w.Error != "" {
		a.Err = errors.New(w.Error)
	}
	return conversation.ToolEvent{Agent: record.Agent, Activity: a}, nil
}
