package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
)

// InspectAgentContext returns only state derived from an accepted log prefix.
func (s *View) InspectAgentContext(ctx context.Context, id identity.ActorID, opts conversation.InspectOptions) (projection.AgentInspection, error) {
	ctx, done, err := s.query(ctx)
	if err != nil {
		return projection.AgentInspection{}, err
	}
	defer done()
	info, err := s.agentInspection(ctx, id)
	if errors.Is(err, projection.ErrNotFound) {
		return info, conversation.ErrAgentNotFound
	}
	if err != nil {
		return info, err
	}
	if opts.Transcript == nil {
		return info, nil
	}
	q := *opts.Transcript
	if q.Limit == 0 {
		q.Limit = agent.DefaultTranscriptLimit
	}
	if q.Limit < 1 || q.Limit > agent.MaxTranscriptLimit {
		return info, agent.ErrInvalidQuery
	}
	if q.Before == 0 {
		q.Before = info.ContextRevision + 1
	}
	entries, err := s.projection.History(id, q.Before, q.Limit)
	if err != nil {
		return info, fmt.Errorf("%w: %v", agent.ErrInvalidQuery, err)
	}
	page := agent.TranscriptPage{Entries: make([]agent.TranscriptEntry, 0, len(entries))}
	if len(entries) > 0 {
		page.HasEarlier = entries[0].Position > 1
	}
	for _, h := range entries {
		e, err := s.ResolveRecord(ctx, eventlog.Record{Session: s.id, Sequence: h.Record.Sequence})
		if err != nil {
			return info, err
		}
		var fact agent.HistoryAppended
		if err = json.Unmarshal(e.Payload, &fact); err != nil {
			return info, err
		}
		page.Entries = append(page.Entries, agent.TranscriptEntry{Position: h.Position, Message: fact.Message})
	}
	info.Transcript = &page
	return info, nil
}
