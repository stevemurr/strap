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
	"github.com/stevemurr/strap/provider"
)

func describeAgent(v conversation.AgentEvent) (eventlog.Data, any, error) {
	var kind string
	var payload any = v.Event
	var output *identity.OutputID
	switch e := v.Event.(type) {
	case agent.Yielded:
		kind = "agent_yielded"
		output = &e.Output
	case agent.InboxDisposition:
		kind = "inbox_disposition"
	case agent.OutputStarted:
		kind = "output_started"
		output = &e.Output
	case agent.OutputDelta:
		e.Channel = provider.NormalizeChannel(e.Channel)
		if !e.Channel.Valid() {
			return eventlog.Data{}, nil, errors.New("invalid output channel")
		}
		payload = e
		kind = "output_delta"
		output = &e.Output
	case agent.HistoryAppended:
		kind = "history_appended"
		output = e.Output
	case agent.OutputFinished:
		kind = "output_finished"
		output = &e.Output
		p := record.OutputFinished{Output: e.Output, Status: e.Status, Bytes: e.Bytes, ReasoningBytes: e.ReasoningBytes, HistoryPosition: e.HistoryPosition, FinishedAt: e.FinishedAt}
		if e.Err != nil {
			code := "generation_failed"
			if errors.Is(e.Err, context.DeadlineExceeded) {
				code = "timeout"
			} else if errors.Is(e.Err, context.Canceled) {
				code = "canceled"
			}
			p.Error = &eventlog.Problem{Code: code, Message: e.Err.Error()}
			var rejected *provider.ToolArgumentsError
			if errors.As(e.Err, &rejected) {
				copy := *rejected
				p.RejectedToolCall = &copy
			}
		}
		payload = p
	default:
		return eventlog.Data{}, nil, fmt.Errorf("unknown agent fact %T", v.Event)
	}

	d := eventlog.Data{Kind: kind, Agent: string(v.Agent), Output: output}
	if output != nil {
		d.Correlation = fmt.Sprintf("%s/output-%d", output.Agent, output.Call)
	}
	return d, payload, nil
}
func decodeAgent(e eventlog.Event) (conversation.Event, error) {
	var fact agent.Event
	switch e.Kind {
	case "agent_yielded":
		var v agent.Yielded
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		fact = v
	case "inbox_disposition":
		var v agent.InboxDisposition
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		fact = v
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
		channel, err := OutputChannel(e.Schema, v.Channel)
		if err != nil {
			return nil, err
		}
		v.Channel = channel
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
		f := agent.OutputFinished{Output: v.Output, Status: v.Status, Bytes: v.Bytes, ReasoningBytes: v.ReasoningBytes, HistoryPosition: v.HistoryPosition, FinishedAt: v.FinishedAt}
		if v.Error != nil {
			f.Err = errors.New(v.Error.Message)
			if v.RejectedToolCall != nil {
				f.Err = recordedOutputError{error: f.Err, rejected: v.RejectedToolCall}
			}
		}
		fact = f
	}
	return conversation.AgentEvent{Agent: identity.ActorID(e.Agent), Event: fact}, nil
}

// Preserve the recorded provider/error-join message while restoring structured
// rejection evidence for errors.As and subsequent re-encoding.
type recordedOutputError struct {
	error
	rejected *provider.ToolArgumentsError
}

func (e recordedOutputError) Unwrap() error { return e.rejected }

// OutputChannel interprets legacy content records without modifying stored bytes.
func OutputChannel(schema int, c provider.OutputChannel) (provider.OutputChannel, error) {
	if !eventlog.SupportedSchema(schema) {
		return "", fmt.Errorf("unsupported event schema %d", schema)
	}
	if schema == 2 {
		if c != "" && c != provider.ChannelContent {
			return "", errors.New("reasoning in content-only schema")
		}
		return provider.ChannelContent, nil
	}
	if !c.Valid() {
		return "", errors.New("invalid output channel")
	}
	return c, nil
}
