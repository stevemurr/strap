package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/provider"
)

// CountTokens measures an exact history revision with this agent's provider and
// tool definitions. It may perform I/O, but never holds the history lock during
// tokenization or changes usage accounting. It remains available after exit.
func (a *Agent) CountTokens(ctx context.Context, revision uint64) (int64, error) {
	counter, ok := a.config.Spec.Provider.(provider.TokenCounter)
	if !ok {
		return 0, fmt.Errorf("provider does not support token counting")
	}
	messages, err := a.thread.messagesAt(revision)
	if err != nil {
		return 0, err
	}
	definitions := append([]provider.ToolDefinition(nil), a.definitions...)
	for i := range definitions {
		definitions[i].Parameters = append(json.RawMessage(nil), definitions[i].Parameters...)
	}
	return counter.CountTokens(ctx, provider.Request{Agent: a.config.ID, Messages: messages, Tools: definitions})
}
