package conversation

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/message"
)

type exitedRecord struct {
	Agent message.ActorID
	Error string
}

// EncodeEvent snapshots host events into the versioned storage envelope. Errors
// are stored as text; they cannot be reconstructed as original Go error values.
func EncodeEvent(e Event) (eventlog.Data, error) {
	var kind string
	var actor message.ActorID
	var payload any = e
	switch v := e.(type) {
	case DiagnosticEvent:
		kind = "diagnostic"
	case MessageEvent:
		kind = "message"
		actor = v.Message.From
	case CommentaryEvent:
		kind = "commentary"
		actor = v.Agent
	case AckEvent:
		kind = "ack"
		actor = v.Receipt.Recipient
	case AgentStarted:
		kind = "agent_started"
		actor = v.Agent.ID
	case AgentExited:
		kind = "agent_exited"
		actor = v.Agent
		x := exitedRecord{Agent: v.Agent}
		if v.Err != nil {
			x.Error = v.Err.Error()
		}
		payload = x
	case AgentStateChanged:
		kind = "agent_state"
		actor = v.Agent
	case ToolEvent:
		kind = "tool"
		actor = v.Agent
	case WorkEvent:
		kind = "work"
	case ToolBatchEvent:
		kind = "tool_batch"
		actor = v.Agent
	case UsageEvent:
		kind = "usage"
		actor = v.Agent
	default:
		return eventlog.Data{}, fmt.Errorf("unsupported event %T", e)
	}
	raw, err := json.Marshal(payload)
	return eventlog.Data{Kind: kind, Agent: string(actor), Payload: raw}, err
}
func decode[T Event](data []byte) (Event, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

// DecodeEvent returns nil for log control records, which callers may inspect
// directly. Domain event payloads are independent from the stored bytes.
func DecodeEvent(e eventlog.Event) (Event, error) {
	switch e.Kind {
	case "diagnostic":
		return decode[DiagnosticEvent](e.Payload)
	case "message":
		return decode[MessageEvent](e.Payload)
	case "commentary":
		return decode[CommentaryEvent](e.Payload)
	case "ack":
		return decode[AckEvent](e.Payload)
	case "agent_started":
		return decode[AgentStarted](e.Payload)
	case "agent_exited":
		var x exitedRecord
		if err := json.Unmarshal(e.Payload, &x); err != nil {
			return nil, err
		}
		v := AgentExited{Agent: x.Agent}
		if x.Error != "" {
			v.Err = errors.New(x.Error)
		}
		return v, nil
	case "agent_state":
		return decode[AgentStateChanged](e.Payload)
	case "tool":
		return decode[ToolEvent](e.Payload)
	case "work":
		return decode[WorkEvent](e.Payload)
	case "tool_batch":
		return decode[ToolBatchEvent](e.Payload)
	case "usage":
		return decode[UsageEvent](e.Payload)
	case "session_closed", "session_started":
		return nil, nil
	case "omitted":
		return nil, fmt.Errorf("event %d payload omitted: %s", e.Sequence, e.Payload)
	default:
		return nil, fmt.Errorf("unknown event kind %q", e.Kind)
	}
}
