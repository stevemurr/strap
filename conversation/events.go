package conversation

import (
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// Creation identifies a newly started, idle agent. Delivery has its own receipt.
type Creation struct {
	AgentID message.ActorID `json:"agent_id"`
}

type AgentInfo struct {
	StateRevision uint64          `json:"state_revision"`
	ID            message.ActorID `json:"agent_id"`
	Parent        message.ActorID `json:"parent"`
	State         agent.State     `json:"state"`
}

// Event is a notification to the host. Acknowledgments do not enter model inboxes
// and therefore cannot generate acknowledgment/reply loops.
type Event interface{ isEvent() }

type MessageEvent struct{ Message message.Message }

func (MessageEvent) isEvent() {}

// CommentaryEvent contains assistant progress text for the host.
// It is never routed to an agent inbox or treated as a completed reply.
type CommentaryEvent struct {
	Output  *identity.OutputID `json:"output,omitempty"`
	Agent   message.ActorID
	Content string
}

func (CommentaryEvent) isEvent() {}

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
	Revision uint64 `json:"state_revision"`
	Agent    message.ActorID
	State    agent.State
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

// ToolBatchEvent marks a complete batch's immutable context boundary. Hosts may
// request a token count asynchronously without delaying the agent loop.
type ToolBatchEvent struct {
	Agent message.ActorID
	Batch agent.ToolBatch
}

func (ToolBatchEvent) isEvent() {}

// UsageEvent reports per-call accounting to the host, never to model inboxes.
type UsageEvent struct {
	Agent       message.ActorID
	Observation agent.UsageObservation
}

func (UsageEvent) isEvent() {}

// DiagnosticEvent is host logging, separate from messages addressed to models.
type DiagnosticEvent struct {
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func (DiagnosticEvent) isEvent() {}

// ContextTokensEvent reports an automatic measurement of an exact history revision.
type ContextTokensEvent struct {
	Agent    message.ActorID `json:"agent"`
	Revision uint64          `json:"revision"`
	Count    int64           `json:"count"`
	Error    string          `json:"error,omitempty"`
}

func (ContextTokensEvent) isEvent() {}

// AgentEvent carries typed runtime output and history facts.
type AgentEvent struct {
	Agent identity.ActorID
	Event agent.Event
}

func (AgentEvent) isEvent() {}
