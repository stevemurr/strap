package harness

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/work"
)

// workView builds a finite passive view from the recorded mutation values. It
// shares visibility policy with work.Store but has no execution collaborators.
func (s *Session) workView(ctx context.Context) (*work.ReadModel, error) {
	if err := s.project(ctx); err != nil {
		return nil, err
	}
	facts := s.projection.Facts("work")
	view := work.NewReadModel()
	for _, cursor := range facts {
		e, err := s.ResolveRecord(ctx, eventlog.Record{Session: s.id, Sequence: cursor.Sequence})
		if err != nil {
			return nil, err
		}
		var v conversation.WorkEvent
		if err = json.Unmarshal(e.Payload, &v); err != nil {
			return nil, err
		}
		if v.Event.Change != nil {
			view.Apply(*v.Event.Change)
		} else {
			c := work.Change{}
			if v.Event.Work.ID != "" {
				c.Works = append(c.Works, v.Event.Work)
			}
			if v.Event.Plan != nil {
				c.Plans = append(c.Plans, *v.Event.Plan)
			}
			view.Apply(c)
		}
	}
	return view, nil
}
