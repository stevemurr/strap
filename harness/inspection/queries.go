package inspection

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
)

type SessionView struct {
	ID            identity.SessionID `json:"id"`
	Through       eventlog.Cursor    `json:"through"`
	Configuration json.RawMessage    `json:"configuration,omitempty"`
	Outcome       *eventlog.Outcome  `json:"outcome,omitempty"`
}

type ToolView struct {
	InvocationID   identity.ToolInvocationID `json:"invocation_id"`
	Agent          identity.ActorID          `json:"agent"`
	Output         *identity.OutputID        `json:"output,omitempty"`
	ProviderCallID string                    `json:"provider_call_id,omitempty"`
	Name           string                    `json:"name"`
	StartedAt      *time.Time                `json:"started_at,omitempty"`
	FinishedAt     *time.Time                `json:"finished_at,omitempty"`
	Error          *eventlog.Problem         `json:"error,omitempty"`
	StartRecord    eventlog.Cursor           `json:"start_record"`
	FinishRecord   *eventlog.Cursor          `json:"finish_record,omitempty"`
}

type OutputView struct {
	projection.OutputView
	Usage        *provider.Usage  `json:"usage,omitempty"`
	StartRecord  eventlog.Cursor  `json:"start_record"`
	FinishRecord *eventlog.Cursor `json:"finish_record,omitempty"`
}

type PageQuery struct {
	After uint64 `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type ToolQuery struct {
	PageQuery
	Agent identity.ActorID `json:"agent,omitempty"`
	Name  string           `json:"name,omitempty"`
}
type OutputQuery struct {
	PageQuery
	Agent identity.ActorID `json:"agent,omitempty"`
}
type AgentQuery = PageQuery

type Page[T any] struct {
	Through eventlog.Cursor `json:"through"`
	Items   []T             `json:"items"`
	Next    uint64          `json:"next"`
	End     bool            `json:"end"`
}
type ToolPage = Page[ToolView]
type OutputPage = Page[OutputView]
type AgentPage = Page[conversation.AgentInspection]

type viewIndex struct {
	tools       map[identity.ToolInvocationID]ToolView
	outputs     map[identity.OutputID]OutputView
	toolOrder   []identity.ToolInvocationID
	outputOrder []identity.OutputID
	agentOrder  []identity.ActorID
	agentStarts map[identity.ActorID]uint64
	callOutputs map[string]identity.OutputID
	config      eventlog.Cursor
	outcome     *eventlog.Outcome
}

func newIndex() viewIndex {
	return viewIndex{tools: map[identity.ToolInvocationID]ToolView{}, outputs: map[identity.OutputID]OutputView{}, agentStarts: map[identity.ActorID]uint64{}, callOutputs: map[string]identity.OutputID{}}
}

// index records compact query metadata; full bodies stay in the source. Legacy
// framed records may lack fields, which remain unavailable rather than inferred.
func (v *View) index(e eventlog.Record) error {
	raw := e.Payload
	frame, framed := record.Frame(raw)
	if framed {
		raw = frame.Control
	}
	switch e.Kind {
	case "session_configured":
		v.indexes.config = e.Cursor()
	case "session_closed":
		var o eventlog.Outcome
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
		v.indexes.outcome = &o
	case "agent_started":
		a := identity.ActorID(e.Agent)
		v.indexes.agentOrder = append(v.indexes.agentOrder, a)
		v.indexes.agentStarts[a] = e.Sequence
	case "history_appended":
		if framed {
			break
		}
		var h agent.HistoryAppended
		if err := json.Unmarshal(raw, &h); err != nil {
			return err
		}
		if h.Output != nil {
			for _, c := range h.Message.ToolCalls {
				v.indexes.callOutputs[e.Agent+"\x00"+c.ID] = *h.Output
			}
		}
	case "output_started":
		var s agent.OutputStarted
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		v.indexes.outputs[s.Output] = OutputView{StartRecord: e.Cursor()}
		v.indexes.outputOrder = append(v.indexes.outputOrder, s.Output)
	case "output_finished":
		var f record.OutputFinished
		if err := json.Unmarshal(raw, &f); err != nil {
			return err
		}
		o := v.indexes.outputs[f.Output]
		c := e.Cursor()
		o.FinishRecord = &c
		v.indexes.outputs[f.Output] = o
	case "usage":
		var u conversation.UsageEvent
		if err := json.Unmarshal(raw, &u); err != nil {
			return err
		}
		id := identity.OutputID{Agent: u.Agent, Call: u.Observation.Call}
		o := v.indexes.outputs[id]
		o.Usage = u.Observation.Usage.Clone()
		v.indexes.outputs[id] = o
	case "tool":
		var t ToolView
		if framed {
			var c struct {
				Invocation string    `json:"invocation_id"`
				Name       string    `json:"name"`
				FinishedAt time.Time `json:"finished_at"`
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				return err
			}
			t.InvocationID = identity.ToolInvocationID(c.Invocation)
			t.Name = c.Name
			if !c.FinishedAt.IsZero() {
				t.FinishedAt = &c.FinishedAt
			}
		} else {
			decoded, err := eventcodec.DecodeEvent(e)
			if err != nil {
				return err
			}
			a := decoded.(conversation.ToolEvent).Activity
			t.InvocationID = identity.ToolInvocationID(a.InvocationID)
			t.Name = a.Call.Name
			t.ProviderCallID = a.Call.ID
			if !a.StartedAt.IsZero() {
				s := a.StartedAt
				t.StartedAt = &s
			}
			if !a.FinishedAt.IsZero() {
				f := a.FinishedAt
				t.FinishedAt = &f
			}
			if a.Err != nil {
				t.Error = &eventlog.Problem{Code: "tool_error", Message: a.Err.Error()}
			}
		}
		t.Agent = identity.ActorID(e.Agent)
		if id, ok := v.indexes.callOutputs[e.Agent+"\x00"+t.ProviderCallID]; ok {
			t.Output = &id
		}
		if t.FinishedAt == nil {
			t.StartRecord = e.Cursor()
			v.indexes.toolOrder = append(v.indexes.toolOrder, t.InvocationID)
		} else {
			prior := v.indexes.tools[t.InvocationID]
			t.StartRecord = prior.StartRecord
			if t.StartedAt == nil {
				t.StartedAt = prior.StartedAt
			}
			if t.ProviderCallID == "" {
				t.ProviderCallID = prior.ProviderCallID
			}
			if t.Output == nil {
				t.Output = prior.Output
			}
			c := e.Cursor()
			t.FinishRecord = &c
		}
		v.indexes.tools[t.InvocationID] = t
	}
	return nil
}

func (v *View) query(ctx context.Context) (context.Context, func(), error) {
	ctx, done, err := v.reader.begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, err = v.log.Head(ctx); err != nil {
		done()
		return nil, nil, err
	}
	return ctx, done, nil
}
func (v *View) Session(ctx context.Context) (SessionView, error) {
	ctx, done, err := v.query(ctx)
	if err != nil {
		return SessionView{}, err
	}
	defer done()
	out := SessionView{ID: identity.SessionID(v.id), Through: v.through}
	if v.indexes.outcome != nil {
		o := *v.indexes.outcome
		out.Outcome = &o
	}
	if v.indexes.config.Sequence != 0 {
		r, err := v.ResolveRecord(ctx, eventlog.Record{Session: v.id, Sequence: v.indexes.config.Sequence})
		if err != nil {
			return out, err
		}
		out.Configuration = append(json.RawMessage(nil), r.Payload...)
	}
	return out, nil
}
func (v *View) InspectTool(ctx context.Context, id identity.ToolInvocationID) (ToolView, error) {
	_, done, err := v.query(ctx)
	if err != nil {
		return ToolView{}, err
	}
	defer done()
	t, ok := v.indexes.tools[id]
	if !ok {
		return ToolView{}, projection.ErrNotFound
	}
	return cloneTool(t), nil
}
func cloneTool(t ToolView) ToolView {
	if t.Output != nil {
		x := *t.Output
		t.Output = &x
	}
	if t.StartedAt != nil {
		x := *t.StartedAt
		t.StartedAt = &x
	}
	if t.FinishedAt != nil {
		x := *t.FinishedAt
		t.FinishedAt = &x
	}
	if t.Error != nil {
		x := *t.Error
		t.Error = &x
	}
	if t.FinishRecord != nil {
		x := *t.FinishRecord
		t.FinishRecord = &x
	}
	return t
}
func (v *View) output(id identity.OutputID) (OutputView, error) {
	p, err := v.projection.Output(id)
	if err != nil {
		return OutputView{}, err
	}
	o := v.indexes.outputs[id]
	o.OutputView = p
	o.Usage = o.Usage.Clone()
	if o.FinishRecord != nil {
		c := *o.FinishRecord
		o.FinishRecord = &c
	}
	return o, nil
}
func (v *View) InspectAgent(ctx context.Context, id identity.ActorID) (conversation.AgentInspection, error) {
	_, done, err := v.query(ctx)
	if err != nil {
		return conversation.AgentInspection{}, err
	}
	defer done()
	return v.projection.AgentInspection(id)
}
func (v *View) validatePage(q PageQuery) (PageQuery, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 1000 {
		return q, fmt.Errorf("%w: limit must be between 1 and 1000", ErrReadQuery)
	}
	if q.After > v.through.Sequence {
		return q, eventlog.ErrFuture
	}
	return q, nil
}
func (v *View) ListTools(ctx context.Context, q ToolQuery) (ToolPage, error) {
	ctx, done, err := v.query(ctx)
	if err != nil {
		return ToolPage{}, err
	}
	defer done()
	pq, err := v.validatePage(q.PageQuery)
	if err != nil {
		return ToolPage{}, err
	}
	page := ToolPage{Through: v.through, Next: pq.After, Items: []ToolView{}}
	for _, id := range v.indexes.toolOrder {
		if err := ctx.Err(); err != nil {
			return ToolPage{}, err
		}
		t := v.indexes.tools[id]
		seq := t.StartRecord.Sequence
		if seq <= pq.After {
			continue
		}
		if len(page.Items) == pq.Limit {
			return page, nil
		}
		page.Next = seq
		if q.Agent != "" && q.Agent != t.Agent || q.Name != "" && q.Name != t.Name {
			continue
		}
		page.Items = append(page.Items, cloneTool(t))
	}
	page.End = true
	page.Next = v.through.Sequence
	return page, nil
}
func (v *View) ListOutputs(ctx context.Context, q OutputQuery) (OutputPage, error) {
	ctx, done, err := v.query(ctx)
	if err != nil {
		return OutputPage{}, err
	}
	defer done()
	pq, err := v.validatePage(q.PageQuery)
	if err != nil {
		return OutputPage{}, err
	}
	page := OutputPage{Through: v.through, Next: pq.After, Items: []OutputView{}}
	for _, id := range v.indexes.outputOrder {
		if err := ctx.Err(); err != nil {
			return OutputPage{}, err
		}
		seq := v.indexes.outputs[id].StartRecord.Sequence
		if seq <= pq.After {
			continue
		}
		if len(page.Items) == pq.Limit {
			return page, nil
		}
		page.Next = seq
		if q.Agent != "" && q.Agent != id.Agent {
			continue
		}
		o, err := v.output(id)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, o)
	}
	page.End = true
	page.Next = v.through.Sequence
	return page, nil
}
func (v *View) ListAgents(ctx context.Context, q AgentQuery) (AgentPage, error) {
	ctx, done, err := v.query(ctx)
	if err != nil {
		return AgentPage{}, err
	}
	defer done()
	q, err = v.validatePage(q)
	if err != nil {
		return AgentPage{}, err
	}
	page := AgentPage{Through: v.through, Next: q.After, Items: []conversation.AgentInspection{}}
	for _, id := range v.indexes.agentOrder {
		if err := ctx.Err(); err != nil {
			return AgentPage{}, err
		}
		seq := v.indexes.agentStarts[id]
		if seq <= q.After {
			continue
		}
		if len(page.Items) == q.Limit {
			return page, nil
		}
		page.Next = seq
		a, err := v.projection.AgentInspection(id)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, a)
	}
	page.End = true
	page.Next = v.through.Sequence
	return page, nil
}
