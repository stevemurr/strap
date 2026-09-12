package tui

import (
	"context"
	"strconv"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

// Values are replaced, never mutated: copy mode retains its original snapshot.
type contextTokens struct {
	revision uint64
	count    int64
	pending  bool
	failed   bool
}

func (c *contextTokens) label() string {
	if c.pending {
		return "counting context…"
	}
	if c.failed {
		return "context tokens unavailable"
	}
	return tokenDigits(c.count) + " context tokens"
}

func tokenDigits(count int64) string {
	digits := strconv.FormatInt(count, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

type tokenSession interface {
	CountAgentTokens(context.Context, message.ActorID, uint64) (int64, error)
}

func (m *model) observeToolBatch(event conversation.ToolBatchEvent) {
	if policy, ok := m.session.(interface{ AutomaticContextTokens() bool }); ok && !policy.AutomaticContextTokens() {
		return
	}
	if len(event.Batch.Calls) == 0 || event.Batch.ContextRevision == 0 {
		return
	}
	key := toolKey{agent: event.Agent, call: event.Batch.Calls[len(event.Batch.Calls)-1]}
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := &m.entries[i]
		if e.label != "Tool" || e.tool != key {
			continue
		}
		if e.tokens != nil {
			return
		} // A duplicate batch event must not reset the measurement.
		e.tokens = &contextTokens{revision: event.Batch.ContextRevision, pending: true}
		if !m.selecting {
			m.renderTranscript(false)
		}
		return
	}
}

func (m *model) observeContextTokens(event conversation.ContextTokensEvent) {
	for i := range m.entries {
		e := &m.entries[i]
		if e.tool.agent != event.Agent || e.tokens == nil || e.tokens.revision != event.Revision {
			continue
		}
		e.tokens = &contextTokens{revision: event.Revision, count: event.Count, failed: event.Error != "" || event.Count < 0}
		if !m.selecting {
			m.renderTranscript(false)
		}
		return
	}
}
