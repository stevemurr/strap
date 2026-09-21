// Package record defines versioned, wire-safe session fact payloads.
package record

import (
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"time"
)

type OutputFinished struct {
	ReasoningBytes   uint64                       `json:"reasoning_bytes"`
	Output           identity.OutputID            `json:"output"`
	Status           agent.OutputStatus           `json:"status"`
	Bytes            uint64                       `json:"bytes"`
	HistoryPosition  *uint64                      `json:"history_position,omitempty"`
	Error            *eventlog.Problem            `json:"error,omitempty"`
	RejectedToolCall *provider.ToolArgumentsError `json:"rejected_tool_call,omitempty"`
	FinishedAt       time.Time                    `json:"finished_at"`
}

type ToolControl struct {
	Execution  *tool.ExecutionBinding `json:"execution,omitempty"`
	Invocation string                 `json:"invocation_id"`
	FinishedAt time.Time              `json:"finished_at"`
	Name       string                 `json:"name"`
}
