// Package record defines versioned, wire-safe session fact payloads.
package record

import (
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/identity"
	"time"
)

type OutputFinished struct {
	Output          identity.OutputID  `json:"output"`
	Status          agent.OutputStatus `json:"status"`
	Bytes           uint64             `json:"bytes"`
	HistoryPosition *uint64            `json:"history_position,omitempty"`
	Error           *eventlog.Problem  `json:"error,omitempty"`
	FinishedAt      time.Time          `json:"finished_at"`
}
