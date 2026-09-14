package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// WakeContext supplies harness-owned state for a new exchange: what the agent
// owns or is assigned, at which revisions, and which records exist. It runs
// once per wake, after admission and after every queued input has been
// consumed, and its result is appended to history before the first model call
// of the exchange. The model therefore never has to remember or guess ids and
// revisions, and cannot write a guess into this record. A nil message means
// there is nothing to add.
type WakeContext func(context.Context, []message.Message) (*message.Message, error)

func (a *Agent) appendWakeContext(ctx context.Context, inputs []message.Message) error {
	if a.config.WakeContext == nil {
		return nil
	}
	copied := make([]message.Message, len(inputs))
	for i, m := range inputs {
		copied[i] = m.Clone()
	}
	state, err := a.config.WakeContext(ctx, copied)
	if err != nil {
		return fmt.Errorf("wake context: %w", err)
	}
	if state == nil {
		return nil
	}
	a.nextWake++
	m := state.Clone()
	if m.ID == "" {
		m.ID = message.MessageID(fmt.Sprintf("%s/wake-%d", a.config.ID, a.nextWake))
	}
	if m.Kind == "" {
		m.Kind = message.Observation
	}
	if m.To == "" {
		m.To = a.config.ID
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("wake context: %w", err)
	}
	_, err = a.appendHistory(provider.Message{Role: "user", Content: content.Text(string(encoded)), Envelope: &m}, nil)
	return err
}
