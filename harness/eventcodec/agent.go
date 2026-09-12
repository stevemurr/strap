package eventcodec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
)

func encodeAgent(v conversation.AgentEvent) (eventlog.Data, error) {
	var kind string
	var payload any = v.Event
	var output *identity.OutputID
	switch e := v.Event.(type) {
	case agent.OutputStarted:
		kind = "output_started"
		output = &e.Output
	case agent.OutputDelta:
		kind = "output_delta"
		output = &e.Output
	case agent.HistoryAppended:
		kind = "history_appended"
		output = e.Output
	case agent.OutputFinished:
		kind = "output_finished"
		output = &e.Output
		p := record.OutputFinished{Output: e.Output, Status: e.Status, Bytes: e.Bytes, HistoryPosition: e.HistoryPosition, FinishedAt: e.FinishedAt}
		if e.Err != nil {
			code := "generation_failed"
			if errors.Is(e.Err, context.DeadlineExceeded) {
				code = "timeout"
			} else if errors.Is(e.Err, context.Canceled) {
				code = "canceled"
			}
			p.Error = &eventlog.Problem{Code: code, Message: e.Err.Error()}
		}
		payload = p
	default:
		return eventlog.Data{}, fmt.Errorf("unknown agent fact %T", v.Event)
	}
	raw, err := json.Marshal(payload)
	d := eventlog.Data{Kind: kind, Agent: string(v.Agent), Payload: raw, Output: output}
	if output != nil {
		d.Correlation = fmt.Sprintf("%s/output-%d", output.Agent, output.Call)
	}
	return d, err
}
func decodeAgent(e eventlog.Event) (conversation.Event, error) {
	var fact agent.Event
	switch e.Kind {
	case "output_started":
		var v agent.OutputStarted
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		fact = v
	case "output_delta":
		var v agent.OutputDelta
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		fact = v
	case "history_appended":
		var v agent.HistoryAppended
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		fact = v
	case "output_finished":
		var v record.OutputFinished
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		f := agent.OutputFinished{Output: v.Output, Status: v.Status, Bytes: v.Bytes, HistoryPosition: v.HistoryPosition, FinishedAt: v.FinishedAt}
		if v.Error != nil {
			f.Err = errors.New(v.Error.Message)
		}
		fact = f
	}
	return conversation.AgentEvent{Agent: identity.ActorID(e.Agent), Event: fact}, nil
}
