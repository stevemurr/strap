package agent

import (
	"context"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"sync"
	"time"
)

// Event contains runtime facts; storage schemas belong to the host adapter.
type Event interface{ isAgentEvent() }
type Reporter interface {
	Publish(context.Context, Event) error
}
type ReporterFunc func(context.Context, Event) error

func (f ReporterFunc) Publish(ctx context.Context, e Event) error { return f(ctx, e) }
func (StateSnapshot) isAgentEvent()                               {}
func (ToolActivity) isAgentEvent()                                {}
func (ToolBatch) isAgentEvent()                                   {}
func (UsageObservation) isAgentEvent()                            {}

type Consumed struct{ Receipt message.Receipt }

func (Consumed) isAgentEvent() {}

type Commentary struct {
	Output identity.OutputID
	Text   string
}

func (Commentary) isAgentEvent() {}

type HistoryAppended struct {
	Position uint64             `json:"position"`
	Message  provider.Message   `json:"message"`
	Output   *identity.OutputID `json:"output,omitempty"`
}

func (HistoryAppended) isAgentEvent() {}

type OutputStatus string

const (
	OutputActive   OutputStatus = "active"
	OutputComplete OutputStatus = "completed"
	OutputFailed   OutputStatus = "failed"
	OutputCanceled OutputStatus = "canceled"
)

type OutputStarted struct {
	Output          identity.OutputID `json:"output"`
	ContextRevision uint64            `json:"context_revision"`
	StartedAt       time.Time         `json:"started_at"`
}

func (OutputStarted) isAgentEvent() {}

type OutputDelta struct {
	Channel provider.OutputChannel `json:"channel"`
	Output  identity.OutputID      `json:"output"`
	Offset  uint64                 `json:"offset"`
	Text    string                 `json:"text"`
}

func (OutputDelta) isAgentEvent() {}

type OutputFinished struct {
	ReasoningBytes  uint64
	Output          identity.OutputID
	Status          OutputStatus
	Bytes           uint64
	HistoryPosition *uint64
	Err             error
	FinishedAt      time.Time
}

func (OutputFinished) isAgentEvent() {}

type reportState struct {
	mu     sync.Mutex
	err    error
	cancel context.CancelFunc
}

func (a *Agent) report(e Event) error {
	if a.config.Reporter == nil {
		return nil
	}
	err := a.config.Reporter.Publish(context.Background(), e)
	if err != nil {
		a.reporting.mu.Lock()
		if a.reporting.err == nil {
			a.reporting.err = err
		}
		if a.reporting.cancel != nil {
			a.reporting.cancel()
		}
		a.reporting.mu.Unlock()
	}
	return err
}
func (a *Agent) reportError() error {
	a.reporting.mu.Lock()
	defer a.reporting.mu.Unlock()
	return a.reporting.err
}
func (a *Agent) appendHistory(m provider.Message, output *identity.OutputID) (uint64, error) {
	position := a.thread.append(m)
	return position, a.report(HistoryAppended{Position: position, Message: provider.CopyMessages([]provider.Message{m})[0], Output: output})
}

// Definitions describes this agent's bound tool schema without exposing tools.
func (a *Agent) Definitions() []provider.ToolDefinition {
	out := append([]provider.ToolDefinition(nil), a.definitions...)
	for i := range out {
		out[i].Parameters = append([]byte(nil), out[i].Parameters...)
	}
	return out
}

// Yielded follows the settled sole-call batch and creates no synthetic reply.
type Yielded struct {
	Output          identity.OutputID `json:"output"`
	CallID          string            `json:"call_id"`
	SettledRevision uint64            `json:"settled_revision"`
}

func (Yielded) isAgentEvent() {}
