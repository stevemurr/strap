package conversation

import (
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// Creation identifies a newly started, idle agent. Delivery has its own receipt.
type Creation struct {
	AgentID message.ActorID `json:"agent_id"`
}

type AgentInfo struct {
	ID     message.ActorID `json:"agent_id"`
	Parent message.ActorID `json:"parent"`
	State  agent.State     `json:"state"`
}

// Event is a notification to the host. Acknowledgments do not enter model inboxes
// and therefore cannot generate acknowledgment/reply loops.
type Event interface{ isEvent() }

type MessageEvent struct{ Message message.Message }

func (MessageEvent) isEvent() {}

type AckEvent struct{ Receipt message.Receipt }

func (AckEvent) isEvent() {}

type AgentStarted struct{ Agent AgentInfo }

func (AgentStarted) isEvent() {}

type AgentExited struct {
	Agent message.ActorID
	Err   error
}

func (AgentExited) isEvent() {}

// AgentStateChanged distinguishes a requested control from its acknowledged state.
type AgentStateChanged struct {
	Agent message.ActorID
	State agent.State
}

func (AgentStateChanged) isEvent() {}

// ToolEvent is host-only execution telemetry. It never enters an agent inbox.
type ToolEvent struct {
	Agent    message.ActorID
	Activity agent.ToolActivity
}

// WorkEvent is a host view supplied by application work orchestration.
type WorkEvent struct{ Event work.Event }

func (WorkEvent) isEvent() {}

func (ToolEvent) isEvent() {}
