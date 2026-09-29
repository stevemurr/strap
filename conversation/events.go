package conversation

import (
	"encoding/json"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
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

type MessageEvent struct {
	Message message.Message `json:"message"`
}

func (MessageEvent) isEvent() {}

// CommentaryEvent contains assistant progress text for the host.
// It is never routed to an agent inbox or treated as a completed reply.
type CommentaryEvent struct {
	Output  *identity.OutputID `json:"output,omitempty"`
	Agent   message.ActorID    `json:"agent"`
	Content string             `json:"content"`
}

func (CommentaryEvent) isEvent() {}

type AckEvent struct {
	Receipt message.Receipt `json:"receipt"`
}

func (AckEvent) isEvent() {}

type AgentStarted struct {
	Agent            AgentInfo                 `json:"agent"`
	Tools            []provider.ToolDefinition `json:"tools"`
	OutputTokenLimit *int64                    `json:"output_token_limit,omitempty"`
}

func (AgentStarted) isEvent() {}

type AgentExited struct {
	Agent message.ActorID `json:"agent"`
	Err   error           `json:"err"`
}

func (AgentExited) isEvent() {}

// AgentStateChanged distinguishes a requested control from its acknowledged state.
type AgentStateChanged struct {
	Revision uint64          `json:"state_revision"`
	Agent    message.ActorID `json:"agent"`
	State    agent.State     `json:"state"`
}

func (AgentStateChanged) isEvent() {}

// ToolEvent is host-only execution telemetry. It never enters an agent inbox.
type ToolEvent struct {
	Agent    message.ActorID    `json:"agent"`
	Activity agent.ToolActivity `json:"activity"`
}

// WorkEvent is a host view supplied by application work orchestration.
type WorkEvent struct {
	Event work.Event `json:"event"`
}

func (WorkEvent) isEvent() {}

func (ToolEvent) isEvent() {}

// ToolBatchEvent marks a complete batch's immutable context boundary. Hosts may
// request a token count asynchronously without delaying the agent loop.
type ToolBatchEvent struct {
	Agent message.ActorID `json:"agent"`
	Batch agent.ToolBatch `json:"batch"`
}

func (ToolBatchEvent) isEvent() {}

// UsageEvent reports per-call accounting to the host, never to model inboxes.
type UsageEvent struct {
	Agent       message.ActorID        `json:"agent"`
	Observation agent.UsageObservation `json:"observation"`
}

func (UsageEvent) isEvent() {}

// DiagnosticEvent is host logging, separate from messages addressed to models.
type DiagnosticEvent struct {
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func (DiagnosticEvent) isEvent() {}

// EnvironmentEvent is one finished call of a tool that reaches outside the
// session (files, shell, language servers, the web) with the result the
// environment returned, before the harness wraps it for the model. A replay
// serves these results in place of the environment.
type EnvironmentEvent struct {
	Agent        message.ActorID `json:"agent"`
	InvocationID string          `json:"invocation_id"`
	Name         string          `json:"name"`
	Arguments    json.RawMessage `json:"arguments"`
	Content      content.Content `json:"content"`
	Captured     content.Content `json:"captured,omitempty"`
	Err          string          `json:"error,omitempty"`
	// Changed lists the workspace files the call created, modified or
	// deleted, relative to the workspace, for the tools that can write:
	// write_file, edit_file and shell. Unscanned reports a workspace too
	// large to compare. Calls that overlap in time share their changes.
	// Scanned reports that the workspace was compared, so an empty Changed
	// means the call changed nothing there, as an auditor's or experimenter's
	// write to its own copy does; records from before it named the file only
	// in the arguments.
	Changed   []string `json:"changed,omitempty"`
	Unscanned bool     `json:"unscanned,omitempty"`
	Scanned   bool     `json:"scanned,omitempty"`
}

func (EnvironmentEvent) isEvent() {}

// ServerMetricsEvent is a snapshot of the model server's own counters and
// gauges, such as vLLM's queue, prefill and decode time and its prefix-cache
// hits. They are server-wide: they describe this session only while it is the
// server's sole client. Phase is start, sample or end.
type ServerMetricsEvent struct {
	Source string             `json:"source"`
	Phase  string             `json:"phase"`
	Values map[string]float64 `json:"values,omitempty"`
	Error  string             `json:"error,omitempty"`
}

func (ServerMetricsEvent) isEvent() {}

// TodosEvent is an agent's whole todo list as it last set it with
// update_todos: the plan it is working through, for the user to follow.
type TodosEvent struct {
	Agent message.ActorID `json:"agent"`
	Todos []Todo          `json:"todos"`
}

// Todo is one entry of a todo list.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress or completed
}

func (TodosEvent) isEvent() {}

// TesterEvent records one adversarial tester run for an agent's change: what
// it was asked to break, the failures the harness reproduced, and the tester's
// whole transcript for analysis.
type TesterEvent struct {
	Agent      message.ActorID    `json:"agent"`
	Request    string             `json:"request"`
	Changed    []string           `json:"changed"`
	Failures   []TestFailure      `json:"failures,omitempty"`
	Calls      int                `json:"calls"`
	Duration   time.Duration      `json:"duration_ns"`
	Error      string             `json:"error,omitempty"`
	Steps      []TesterStep       `json:"steps,omitempty"`
	Transcript []provider.Message `json:"transcript,omitempty"`
}

// TesterStep times one tester model call and the tools it asked for.
type TesterStep struct {
	At    time.Duration `json:"at_ns"`    // Since the run started.
	Model time.Duration `json:"model_ns"` // The model call.
	Tools time.Duration `json:"tools_ns"` // Its tool calls, one after another.
	Calls []string      `json:"calls,omitempty"`
}

// TestFailure is a test the tester reported and the harness saw fail.
type TestFailure struct {
	Path        string `json:"path"`
	Command     string `json:"command"`
	Requirement string `json:"requirement"`
	Test        string `json:"test"`   // The test file, bounded.
	Output      string `json:"output"` // The failing run's output, bounded.
}

func (TesterEvent) isEvent() {}

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
	Agent identity.ActorID `json:"agent"`
	Event agent.Event      `json:"event"`
}

func (AgentEvent) isEvent() {}

// AgentRegistered is an application fact; the controller does not select roles.
type AgentRegistered struct {
	Registration roster.Registration `json:"registration"`
}

func (AgentRegistered) isEvent() {}
