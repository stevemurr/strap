package tui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

type countedTokens struct {
	agent    message.ActorID
	revision uint64
	count    int64
	err      error
}

type tokenSession interface {
	CountAgentTokens(context.Context, message.ActorID, uint64) (int64, error)
}

func (m *model) countToolBatch(event conversation.ToolBatchEvent) tea.Cmd {
	if len(event.Batch.Calls) == 0 || event.Batch.ContextRevision == 0 {
		return nil
	}
	key := toolKey{agent: event.Agent, call: event.Batch.Calls[len(event.Batch.Calls)-1]}
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := &m.entries[i]
		if e.label != "Tool" || e.tool != key {
			continue
		}
		if e.tokens != nil {
			return nil
		} // A duplicate batch event must not recount.
		e.tokens = &contextTokens{revision: event.Batch.ContextRevision, pending: true}
		if !m.selecting {
			m.renderTranscript(false)
		}
		session, ok := m.session.(tokenSession)
		return func() tea.Msg {
			result := countedTokens{agent: event.Agent, revision: event.Batch.ContextRevision}
			if !ok {
				result.err = fmt.Errorf("session does not support token counting")
				return result
			}
			// Display telemetry must not inherit a potentially hour-long model
			// deadline. Bubble Tea runs this command away from the input loop.
			ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
			defer cancel()
			result.count, result.err = session.CountAgentTokens(ctx, event.Agent, event.Batch.ContextRevision)
			return result
		}
	}
	return nil // The corresponding tool was cleared from the display.
}

func (m *model) finishTokenCount(result countedTokens) {
	for i := range m.entries {
		e := &m.entries[i]
		if e.tool.agent != result.agent || e.tokens == nil || e.tokens.revision != result.revision {
			continue
		}
		e.tokens = &contextTokens{revision: result.revision, count: result.count, failed: result.err != nil || result.count < 0}
		if !m.selecting {
			m.renderTranscript(false)
		}
		return
	}
}
