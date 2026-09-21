package agent

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/provider"
)

var ErrTokenCountingUnsupported = errors.New("provider does not support token counting")

// ContextRevision identifies the current retained history, including replies
// and tool results. Queued inbox messages have not entered that history yet.
func (a *Agent) ContextRevision() uint64 {
	a.thread.mu.RLock()
	defer a.thread.mu.RUnlock()
	return a.thread.revision
}

// OutputTokenLimit reads optional provider metadata without model execution.
func (a *Agent) OutputTokenLimit() *int64 {
	if p, ok := a.config.Spec.Provider.(provider.OutputTokenLimiter); ok {
		if limit := p.OutputTokenLimit(); limit != nil && *limit > 0 {
			copy := *limit
			return &copy
		}
	}
	return nil
}

// CountTokens measures an exact history revision with this agent's provider and
// tool definitions. It may perform I/O, but never holds the history lock during
// tokenization or changes usage accounting. It remains available after exit.
func (a *Agent) CountTokens(ctx context.Context, revision uint64) (int64, error) {
	counter, ok := a.config.Spec.Provider.(provider.TokenCounter)
	if !ok {
		return 0, ErrTokenCountingUnsupported
	}
	messages, err := a.thread.messagesAt(revision)
	if err != nil {
		return 0, err
	}
	return counter.CountTokens(ctx, provider.Request{Agent: a.config.ID, Messages: messages, Tools: a.Definitions()})
}
