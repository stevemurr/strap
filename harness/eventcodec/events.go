// Package eventcodec snapshots runtime notifications into versioned session records.
package eventcodec

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/message"
)

type exitedRecord struct {
	Agent message.ActorID `json:"agent"`
	Error string          `json:"error"`
}

// EncodeEvent snapshots host events into the versioned storage envelope. Errors
// are stored as text; they cannot be reconstructed as original Go error values.
func describe(e conversation.Event) (eventlog.Data, any, error) {
	var kind, correlation string
	var actor message.ActorID
	var payload any = e
	switch v := e.(type) {
	case conversation.AgentEvent:
		return describeAgent(v)
	case conversation.ContextTokensEvent:
		kind = "context_tokens"
		actor = v.Agent
	case conversation.DiagnosticEvent:
		kind = "diagnostic"
	case conversation.MessageEvent:
		kind = "message"
		actor = v.Message.From
	case conversation.CommentaryEvent:
		kind = "commentary"
		actor = v.Agent
	case conversation.AckEvent:
		kind = "ack"
		actor = v.Receipt.Recipient
	case conversation.AgentStarted:
		kind = "agent_started"
		actor = v.Agent.ID
	case conversation.AgentExited:
		kind = "agent_exited"
		actor = v.Agent
		x := exitedRecord{Agent: v.Agent}
		if v.Err != nil {
			x.Error = v.Err.Error()
		}
		payload = x
	case conversation.AgentStateChanged:
		kind = "agent_state"
		actor = v.Agent
	case conversation.ToolEvent:
		payload = toolRecord{Agent: v.Agent, Activity: encodeTool(v.Activity)}
		correlation = v.Activity.InvocationID
		kind = "tool"
		actor = v.Agent
	case conversation.WorkEvent:
		kind = "work"
	case conversation.ToolBatchEvent:
		kind = "tool_batch"
		actor = v.Agent
	case conversation.UsageEvent:
		kind = "usage"
		actor = v.Agent
	default:
		return eventlog.Data{}, nil, fmt.Errorf("unsupported event %T", e)
	}

	d := eventlog.Data{Kind: kind, Correlation: correlation, Agent: string(actor)}
	switch v := e.(type) {
	case conversation.MessageEvent:
		d.Message = v.Message.ID
		d.Output = v.Message.Output
	case conversation.CommentaryEvent:
		d.Output = v.Output
	case conversation.AckEvent:
		d.Message = v.Receipt.MessageID
	}
	return d, payload, nil
}
func decode[T conversation.Event](data []byte) (conversation.Event, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

// DecodeEvent returns nil for log control records, which callers may inspect
// directly. Domain event payloads are independent from the stored bytes.
func DecodeEvent(e eventlog.Event) (conversation.Event, error) {
	if !eventlog.SupportedSchema(e.Schema) {
		return nil, fmt.Errorf("unsupported event schema %d", e.Schema)
	}
	if _, ok := record.Frame(e.Payload); ok {
		return nil, errors.New("record requires content resolution")
	}
	switch e.Kind {
	case "output_started", "output_delta", "output_finished", "history_appended":
		return decodeAgent(e)
	case "context_tokens":
		return decode[conversation.ContextTokensEvent](e.Payload)
	case "diagnostic":
		return decode[conversation.DiagnosticEvent](e.Payload)
	case "message":
		v, err := decode[conversation.MessageEvent](e.Payload)
		if err != nil {
			return nil, err
		}
		m := v.(conversation.MessageEvent)
		m.Message.Output = e.Output
		return m, nil
	case "commentary":
		return decode[conversation.CommentaryEvent](e.Payload)
	case "ack":
		return decode[conversation.AckEvent](e.Payload)
	case "agent_started":
		return decode[conversation.AgentStarted](e.Payload)
	case "agent_exited":
		var x exitedRecord
		if err := json.Unmarshal(e.Payload, &x); err != nil {
			return nil, err
		}
		v := conversation.AgentExited{Agent: x.Agent}
		if x.Error != "" {
			v.Err = errors.New(x.Error)
		}
		return v, nil
	case "agent_state":
		return decode[conversation.AgentStateChanged](e.Payload)
	case "tool":
		return decodeTool(e.Payload)
	case "work":
		return decode[conversation.WorkEvent](e.Payload)
	case "tool_batch":
		return decode[conversation.ToolBatchEvent](e.Payload)
	case "usage":
		return decode[conversation.UsageEvent](e.Payload)
	case "content_chunk", "session_closed", "session_started", "session_configured":
		return nil, nil
	case "omitted":
		return nil, fmt.Errorf("event %d payload omitted: %s", e.Sequence, e.Payload)
	default:
		return nil, fmt.Errorf("unknown event kind %q", e.Kind)
	}
}

func EncodeEvent(e conversation.Event) (eventlog.Data, error) {
	d, p, err := describe(e)
	if err != nil {
		return d, err
	}
	d.Payload, err = json.Marshal(p)
	return d, err
}
