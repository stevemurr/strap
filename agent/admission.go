package agent

import (
	"context"
	"github.com/stevemurr/strap/message"
)

// InboxAdmission is application policy for a new exchange only. It receives
// detached envelopes and cannot change history, reply routing or tool authority.
type InboxAdmission func(context.Context, []message.Message) (InboxDecision, error)
type InboxDecision struct {
	Wake    bool   `json:"wake"`
	Session string `json:"session,omitempty"`
	Through uint64 `json:"through,omitempty"`
}
type InboxDisposition struct {
	Messages []message.MessageID `json:"messages"`
	Decision InboxDecision       `json:"decision"`
}

func (InboxDisposition) isAgentEvent() {}
func (a *Agent) admit(ctx context.Context, incoming []message.Message) (bool, error) {
	if a.config.AdmitInbox == nil {
		for _, m := range incoming {
			if m.Kind != message.Observation {
				return true, nil
			}
		}
		return false, nil
	}
	copied := make([]message.Message, len(incoming))
	ids := make([]message.MessageID, len(incoming))
	for i, m := range incoming {
		copied[i] = m.Clone()
		ids[i] = m.ID
	}
	d, err := a.config.AdmitInbox(ctx, copied)
	if err != nil {
		return false, err
	}
	if err = a.report(InboxDisposition{Messages: ids, Decision: d}); err != nil {
		return false, err
	}
	return d.Wake, nil
}
