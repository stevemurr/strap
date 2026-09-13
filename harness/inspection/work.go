package inspection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"io"
	"sort"
)

func (v *View) workModel(ctx context.Context) (*work.ReadModel, map[work.ID]uint64, error) {
	ctx, done, err := v.query(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	model := work.NewReadModel()
	first := map[work.ID]uint64{}
	for _, cursor := range v.projection.Facts("work") {
		e, err := v.ResolveRecord(ctx, eventlog.Record{Session: v.id, Sequence: cursor.Sequence})
		if err != nil {
			return nil, nil, err
		}
		var fact conversation.WorkEvent
		if err = json.Unmarshal(e.Payload, &fact); err != nil {
			return nil, nil, err
		}
		change := fact.Event.Change
		if change == nil {
			change = &work.Change{}
			if fact.Event.Work.ID != "" {
				change.Works = append(change.Works, fact.Event.Work)
			}
			if fact.Event.Plan != nil {
				change.Plans = append(change.Plans, *fact.Event.Plan)
			}
		}
		for _, w := range change.Works {
			if _, ok := first[w.ID]; !ok {
				first[w.ID] = cursor.Sequence
			}
		}
		model.Apply(*change)
	}
	return model, first, nil
}
func (v *View) agentInspection(ctx context.Context, id identity.ActorID) (projection.AgentInspection, error) {
	a, err := v.projection.AgentInspection(id)
	if err != nil {
		return projection.AgentInspection{}, err
	}
	model, _, err := v.workModel(ctx)
	if err != nil {
		return projection.AgentInspection{}, err
	}
	return v.projection.Enrich(a, model.Works()), nil
}
func (v *View) InspectWork(ctx context.Context, actor identity.ActorID, id work.ID) (work.Inspection, error) {
	model, _, err := v.workModel(ctx)
	if err != nil {
		return work.Inspection{}, err
	}
	return model.InspectWork(actor, id)
}

type workCursor struct {
	Session  string         `json:"session"`
	Prefix   uint64         `json:"prefix"`
	Filters  work.ListQuery `json:"filters"`
	Sequence uint64         `json:"sequence"`
	ID       work.ID        `json:"id"`
}

func decodeWorkQuery(q work.ListQuery) (workCursor, error) {
	c := workCursor{Filters: q}
	if q.Limit < 0 || q.Limit > 100 {
		return c, fmt.Errorf("%w: limit must be 1–100", work.ErrInvalid)
	}
	if q.Cursor != "" {
		if q.Assignee != "" || q.Kind != "" || q.State != "" {
			return c, fmt.Errorf("%w: cursor cannot be combined with filters", work.ErrInvalid)
		}
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil {
			return c, fmt.Errorf("%w: invalid cursor", work.ErrInvalid)
		}
		c = workCursor{}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if e = d.Decode(&c); e != nil || c.Session == "" || c.Prefix == 0 || c.Sequence == 0 || c.ID == "" || c.Sequence > c.Prefix || c.Filters.Cursor != "" || c.Filters.Limit != 0 {
			return c, fmt.Errorf("%w: invalid cursor", work.ErrInvalid)
		}
		var trailing any
		if d.Decode(&trailing) != io.EOF {
			return c, fmt.Errorf("%w: trailing cursor data", work.ErrInvalid)
		}
	}
	switch c.Filters.Kind {
	case "", work.Implementation, work.AuditWork, work.Repair:
	default:
		return c, fmt.Errorf("%w: invalid kind", work.ErrInvalid)
	}
	switch c.Filters.State {
	case "", work.Active, work.NeedsCheck, work.ChangesRequested, work.Checking, work.Accepted, work.Closed, work.Cancelled:
	default:
		return c, fmt.Errorf("%w: invalid state", work.ErrInvalid)
	}
	return c, nil
}

// ListWork captures the prefix in its cursor; continuation never reads newer state.
func (r *Reader) ListWork(ctx context.Context, actor identity.ActorID, q work.ListQuery) (work.ListPage, error) {
	c, err := decodeWorkQuery(q)
	if err != nil {
		return work.ListPage{}, err
	}
	through := eventlog.Cursor{}
	if q.Cursor != "" {
		through = eventlog.Cursor{Session: c.Session, Sequence: c.Prefix}
	}
	v, err := r.At(ctx, through)
	if err != nil {
		return work.ListPage{}, err
	}
	return v.ListWork(ctx, actor, q)
}
func (v *View) ListWork(ctx context.Context, actor identity.ActorID, q work.ListQuery) (work.ListPage, error) {
	c, err := decodeWorkQuery(q)
	if err != nil {
		return work.ListPage{}, err
	}
	a, err := v.projection.AgentInspection(actor)
	if err != nil || actor == "" || a.Parent != message.User {
		return work.ListPage{}, work.ErrForbidden
	}
	if q.Cursor != "" && (c.Session != v.id || c.Prefix != v.through.Sequence) {
		return work.ListPage{}, fmt.Errorf("%w: cursor does not match view prefix", work.ErrInvalid)
	}
	model, first, err := v.workModel(ctx)
	if err != nil {
		return work.ListPage{}, err
	}
	entries := model.Works()
	sort.Slice(entries, func(i, j int) bool {
		if first[entries[i].ID] != first[entries[j].ID] {
			return first[entries[i].ID] > first[entries[j].ID]
		}
		return entries[i].ID < entries[j].ID
	})
	filter := func(w work.Work) bool {
		return w.Owner == actor && (c.Filters.Assignee == "" || c.Filters.Assignee == w.Assignee) && (c.Filters.Kind == "" || c.Filters.Kind == w.Kind) && (c.Filters.State == "" || c.Filters.State == w.State)
	}
	if q.Cursor != "" {
		valid := false
		for _, w := range entries {
			if w.ID == c.ID && first[w.ID] == c.Sequence && filter(w) {
				valid = true
			}
		}
		if !valid {
			return work.ListPage{}, fmt.Errorf("%w: missing cursor ordering key", work.ErrInvalid)
		}
	}
	limit := q.Limit
	if limit == 0 {
		limit = 20
	}
	page := work.ListPage{Items: []work.Summary{}}
	var last work.Work
	for _, w := range entries {
		if err := ctx.Err(); err != nil {
			return work.ListPage{}, err
		}
		if !filter(w) || q.Cursor != "" && (first[w.ID] > c.Sequence || first[w.ID] == c.Sequence && w.ID <= c.ID) {
			continue
		}
		if len(page.Items) == limit {
			c.Session = v.id
			c.Prefix = v.through.Sequence
			c.Sequence = first[last.ID]
			c.ID = last.ID
			c.Filters.Cursor = ""
			c.Filters.Limit = 0
			b, _ := json.Marshal(c)
			page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			return page, nil
		}
		preview := []rune(w.Task)
		if len(preview) > 240 {
			preview = preview[:240]
		}
		page.Items = append(page.Items, work.Summary{ID: w.ID, Kind: w.Kind, State: w.State, Revision: w.Revision, Owner: w.Owner, Assignee: w.Assignee, ParentID: w.ParentID, RequestedByAuditID: w.RequestedByAuditID, LatestSubmissionID: w.LatestSubmissionID, TaskPreview: string(preview)})
		last = w
	}
	return page, nil
}

func (v *View) Agents(ctx context.Context) ([]projection.AgentInfo, error) {
	model, _, err := v.workModel(ctx)
	if err != nil {
		return nil, err
	}
	works := model.Works()
	out := make([]projection.AgentInfo, 0, len(v.indexes.agentOrder))
	for _, id := range v.indexes.agentOrder {
		a, err := v.projection.AgentInspection(id)
		if err != nil {
			return nil, err
		}
		out = append(out, v.projection.Enrich(a, works).Info())
	}
	return out, nil
}
