// Package projection reduces immutable session records without execution effects.
package projection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
	"hash"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrNotFound = errors.New("projection entity not found")

type OutputView struct {
	ID              identity.OutputID  `json:"id"`
	Status          agent.OutputStatus `json:"status"`
	ContextRevision uint64             `json:"context_revision"`
	HistoryPosition *uint64            `json:"history_position,omitempty"`
	TextBytes       uint64             `json:"text_bytes"`
	Through         eventlog.Cursor    `json:"through"`
	Error           *eventlog.Problem  `json:"error,omitempty"`
}
type HistoryView struct {
	Position uint64             `json:"position"`
	Role     string             `json:"role"`
	Output   *identity.OutputID `json:"output,omitempty"`
	Record   eventlog.Cursor    `json:"record"`
}
type MessageView struct {
	ID     identity.MessageID  `json:"id"`
	From   identity.ActorID    `json:"from"`
	To     identity.ActorID    `json:"to"`
	Kind   message.MessageKind `json:"kind"`
	Output *identity.OutputID  `json:"output,omitempty"`
	Record eventlog.Cursor     `json:"record"`
}
type chunkState struct {
	ref eventlog.ContentRef
	sum hash.Hash
}
type toolState struct {
	Agent     identity.ActorID
	Finished  bool
	StartedAt time.Time
}
type WorkView struct {
	record.WorkHeader
	Through eventlog.Cursor `json:"through"`
	Record  eventlog.Cursor `json:"record"`
}
type Projector struct {
	workViews  map[work.ID]WorkView
	toolStates map[string]toolState
	calls      map[identity.ActorID]uint64
	messageIDs map[identity.MessageID]MessageView
	workEvents map[work.EventID]bool
	usage      map[identity.ActorID]agent.UsageSnapshot
	limits     map[identity.ActorID]*int64
	mu         sync.RWMutex
	session    string
	cursor     eventlog.Cursor
	closed     bool
	outputs    map[identity.OutputID]OutputView
	agents     map[identity.ActorID]conversation.AgentInfo
	histories  map[identity.ActorID][]HistoryView
	messages   []MessageView
	receipts   map[identity.MessageID]message.Receipt
	chunks     map[identity.ContentID]*chunkState
	contents   map[identity.ContentID]eventlog.ContentRef
	facts      map[string][]eventlog.Cursor
}

func New(session identity.SessionID) *Projector {
	return &Projector{workViews: map[work.ID]WorkView{}, toolStates: map[string]toolState{}, calls: map[identity.ActorID]uint64{}, messageIDs: map[identity.MessageID]MessageView{}, workEvents: map[work.EventID]bool{}, usage: map[identity.ActorID]agent.UsageSnapshot{}, limits: map[identity.ActorID]*int64{}, session: string(session), cursor: eventlog.Cursor{Session: string(session)}, outputs: map[identity.OutputID]OutputView{}, agents: map[identity.ActorID]conversation.AgentInfo{}, histories: map[identity.ActorID][]HistoryView{}, receipts: map[identity.MessageID]message.Receipt{}, chunks: map[identity.ContentID]*chunkState{}, contents: map[identity.ContentID]eventlog.ContentRef{}, facts: map[string][]eventlog.Cursor{}}
}
func (p *Projector) Cursor() eventlog.Cursor { p.mu.RLock(); defer p.mu.RUnlock(); return p.cursor }
func (p *Projector) Apply(e eventlog.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.Session != p.session {
		return eventlog.ErrSession
	}
	if e.Sequence <= p.cursor.Sequence {
		return nil
	}
	if e.Sequence != p.cursor.Sequence+1 {
		return errors.New("projection sequence gap")
	}
	if e.Schema != eventlog.SchemaVersion {
		return errors.New("unsupported session record schema")
	}
	if p.closed {
		return errors.New("record after session terminal")
	}
	if !json.Valid(e.Payload) {
		return errors.New("invalid record JSON")
	}
	original := e.Payload
	framed, isFramed := record.Frame(original)
	if isFramed {
		ref := *framed.Content
		c := p.chunks[ref.ID]
		if c == nil || c.ref.ID != ref.ID || c.ref.First != ref.First || c.ref.Last != ref.Last || c.ref.Bytes != ref.Bytes || hex.EncodeToString(c.sum.Sum(nil)) != ref.SHA256 || ref.Last.Sequence >= e.Sequence || ref.First.Session != p.session || ref.Last.Session != p.session {
			return errors.New("invalid content reference")
		}
		if old, ok := p.contents[ref.ID]; ok && old != ref {
			return errors.New("conflicting content identity")
		}
		e.Payload = framed.Control
	}
	var commit func()
	actor := identity.ActorID(e.Agent)
	switch e.Kind {
	case "session_started":
		var v struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if e.Sequence != 1 || v.ID != p.session {
			return errors.New("invalid session start")
		}
	case "session_configured":
		if e.Sequence == 1 {
			return errors.New("missing session start")
		}
	case "session_closed":
		var o eventlog.Outcome
		if err := json.Unmarshal(e.Payload, &o); err != nil {
			return err
		}
		commit = func() { p.closed = true }
	case "content_chunk":
		var v record.ContentChunk
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if v.ID == "" || len(v.Data) == 0 {
			return errors.New("empty content chunk")
		}
		c := p.chunks[v.ID]
		if c == nil {
			c = &chunkState{ref: eventlog.ContentRef{ID: v.ID, First: e.Cursor()}, sum: sha256.New()}
		}
		if _, ok := p.contents[v.ID]; ok || v.Offset != c.ref.Bytes {
			return errors.New("invalid content offset")
		}
		commit = func() {
			c.sum.Write(v.Data)
			c.ref.Bytes += uint64(len(v.Data))
			c.ref.Last = e.Cursor()
			p.chunks[v.ID] = c
		}
	case "output_started":
		var v agent.OutputStarted
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if v.Output.Agent != actor || v.Output.Call != p.calls[actor]+1 || v.StartedAt.IsZero() {
			return errors.New("invalid output start")
		}
		if _, ok := p.agents[actor]; !ok {
			return errors.New("output before agent start")
		}
		if _, ok := p.outputs[v.Output]; ok {
			return errors.New("duplicate output start")
		}
		if v.ContextRevision != uint64(len(p.histories[actor])) {
			return errors.New("output context revision mismatch")
		}
		commit = func() {
			p.calls[actor] = v.Output.Call
			p.outputs[v.Output] = OutputView{ID: v.Output, Status: agent.OutputActive, ContextRevision: v.ContextRevision}
		}
	case "output_delta":
		var v agent.OutputDelta
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		o, ok := p.outputs[v.Output]
		if !ok || o.Status != agent.OutputActive || v.Offset != o.TextBytes || v.Text == "" || !utf8.ValidString(v.Text) || v.Output.Agent != actor {
			return errors.New("invalid output delta")
		}
		commit = func() { o.TextBytes += uint64(len(v.Text)); p.outputs[v.Output] = o }
	case "history_appended":
		var v agent.HistoryAppended
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		role := v.Message.Role
		if isFramed {
			var c struct {
				Role string `json:"role"`
			}
			if err := json.Unmarshal(e.Payload, &c); err != nil {
				return err
			}
			role = c.Role
		}
		if _, ok := p.agents[actor]; !ok {
			return errors.New("history before agent start")
		}
		if v.Position != uint64(len(p.histories[actor]))+1 {
			return errors.New("history position gap")
		}
		if role != "system" && role != "user" && role != "assistant" && role != "tool" {
			return errors.New("invalid history role")
		}
		if v.Output != nil {
			o, ok := p.outputs[*v.Output]
			if !ok || o.Status != agent.OutputActive || role != "assistant" || v.Output.Agent != actor {
				return errors.New("history output mismatch")
			}
		}
		h := HistoryView{Position: v.Position, Role: role, Output: v.Output, Record: e.Cursor()}
		commit = func() { p.histories[actor] = append(p.histories[actor], h) }
	case "output_finished":
		var v record.OutputFinished
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		o, ok := p.outputs[v.Output]
		if !ok || v.Output.Agent != actor || o.Status != agent.OutputActive || v.Bytes != o.TextBytes || v.FinishedAt.IsZero() {
			return errors.New("invalid output finish")
		}
		if v.Status == agent.OutputComplete {
			if v.HistoryPosition == nil || *v.HistoryPosition == 0 || *v.HistoryPosition > uint64(len(p.histories[actor])) || v.Error != nil {
				return errors.New("invalid output commitment")
			}
			h := p.histories[actor][*v.HistoryPosition-1]
			if h.Output == nil || *h.Output != v.Output {
				return errors.New("history belongs to another output")
			}
		} else if (v.Status != agent.OutputFailed && v.Status != agent.OutputCanceled) || v.HistoryPosition != nil || v.Error == nil {
			return errors.New("invalid output failure")
		}
		commit = func() {
			o.Status = v.Status
			o.HistoryPosition = v.HistoryPosition
			o.Error = v.Error
			p.outputs[v.Output] = o
		}
	case "agent_started":
		var v conversation.AgentStarted
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if v.Agent.ID == "" || v.Agent.ID != actor || v.Agent.StateRevision != 1 {
			return errors.New("invalid agent start")
		}
		if _, ok := p.agents[actor]; ok {
			return errors.New("duplicate agent")
		}
		commit = func() { p.agents[actor] = v.Agent; p.limits[actor] = v.OutputTokenLimit }
	case "agent_state":
		var v conversation.AgentStateChanged
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		a, ok := p.agents[actor]
		if !ok || v.Agent != actor || v.Revision != a.StateRevision+1 {
			return errors.New("agent state revision gap")
		}
		switch v.State {
		case agent.Idle, agent.Running, agent.PauseRequested, agent.Paused, agent.StopRequested, agent.Stopped, agent.Failed:
		default:
			return errors.New("invalid agent state")
		}
		commit = func() { a.State = v.State; a.StateRevision = v.Revision; p.agents[actor] = a }
	case "message":
		v, err := eventcodec.DecodeEvent(e)
		if err != nil {
			return err
		}
		m := v.(conversation.MessageEvent).Message
		if m.ID == "" {
			return errors.New("message requires stable identity")
		}
		if _, ok := p.messageIDs[m.ID]; ok {
			return errors.New("duplicate message")
		}
		commit = func() {
			v := MessageView{ID: m.ID, From: m.From, To: m.To, Kind: m.Kind, Output: e.Output, Record: e.Cursor()}
			p.messages = append(p.messages, v)
			p.messageIDs[m.ID] = v
		}
	case "ack":
		var v conversation.AckEvent
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		r := v.Receipt
		_, exists := p.messageIDs[r.MessageID]
		if !exists {
			return errors.New("receipt before message")
		}
		if r.Status != message.Queued && r.Status != message.Consumed && r.Status != message.Undelivered {
			return errors.New("invalid receipt status")
		}
		commit = func() { p.receipts[r.MessageID] = r }
	case "usage":
		var v conversation.UsageEvent
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		u := p.usage[actor]
		if v.Agent != actor || v.Observation.Call != u.Calls+1 {
			return errors.New("invalid usage call sequence")
		}
		if _, ok := p.outputs[identity.OutputID{Agent: actor, Call: v.Observation.Call}]; !ok {
			return errors.New("usage without output")
		}
		observation := v.Observation
		u.Calls++
		u.Latest = &observation
		if observation.Usage != nil && observation.Usage.InputTokens != nil {
			u.InputTokens += *observation.Usage.InputTokens
		} else {
			u.MissingInputCalls++
		}
		if observation.Usage != nil && observation.Usage.OutputTokens != nil {
			u.OutputTokens += *observation.Usage.OutputTokens
		} else {
			u.MissingOutputCalls++
		}
		commit = func() { p.usage[actor] = u }

	case "tool":
		var invocation string
		var started, finished time.Time
		if isFramed {
			var v struct {
				Invocation string    `json:"invocation_id"`
				FinishedAt time.Time `json:"finished_at"`
			}
			if err := json.Unmarshal(e.Payload, &v); err != nil {
				return err
			}
			invocation = v.Invocation
			finished = v.FinishedAt
		} else {
			v, err := eventcodec.DecodeEvent(e)
			if err != nil {
				return err
			}
			t := v.(conversation.ToolEvent)
			if t.Agent != actor {
				return errors.New("tool agent mismatch")
			}
			invocation = t.Activity.InvocationID
			started = t.Activity.StartedAt
			finished = t.Activity.FinishedAt
		}
		if invocation == "" {
			return errors.New("tool requires invocation identity")
		}
		prior, exists := p.toolStates[invocation]
		if finished.IsZero() {
			if exists {
				return errors.New("duplicate tool start")
			}
			commit = func() { p.toolStates[invocation] = toolState{Agent: actor, StartedAt: started} }
		} else {
			if !exists || prior.Finished || prior.Agent != actor {
				return errors.New("tool finish without matching start")
			}
			commit = func() { prior.Finished = true; p.toolStates[invocation] = prior }
		}

	case "work":
		var c record.WorkControl
		if isFramed {
			if err := json.Unmarshal(e.Payload, &c); err != nil {
				return err
			}
		} else {
			var v conversation.WorkEvent
			if err := json.Unmarshal(e.Payload, &v); err != nil {
				return err
			}
			c = record.DescribeWork(v.Event)
		}
		if c.ID == "" || p.workEvents[c.ID] {
			return errors.New("missing or duplicate work event identity")
		}
		switch c.Kind {
		case work.PlanChanged, work.WorkAssigned, work.WorkReassigned, work.WorkCancelled, work.ProgressChanged, work.ReviewRequested, work.AuditCompleted:
		default:
			return errors.New("invalid work event kind")
		}
		for _, w := range c.Works {
			if w.ID == "" || w.Revision == 0 || w.Revision < p.workViews[w.ID].Revision {
				return errors.New("invalid work revision")
			}
			switch w.Kind {
			case work.Implementation, work.AuditWork, work.Repair:
			default:
				return errors.New("invalid work kind")
			}
			switch w.State {
			case work.Active, work.NeedsCheck, work.Checking, work.ChangesRequested, work.Accepted, work.Closed, work.Cancelled:
			default:
				return errors.New("invalid work state")
			}
		}
		for _, p := range c.Plans {
			if p.ID == "" || p.Revision == 0 {
				return errors.New("invalid plan header")
			}
		}
		for _, s := range c.Submissions {
			if s.ID == "" || s.Work == "" {
				return errors.New("invalid submission header")
			}
		}
		for _, a := range c.Audits {
			if a.ID == "" || a.Work == "" || a.Submission == "" || a.Verdict != work.Pass && a.Verdict != work.Fail {
				return errors.New("invalid audit header")
			}
		}
		commit = func() {
			p.workEvents[c.ID] = true
			for _, w := range c.Works {
				p.workViews[w.ID] = WorkView{WorkHeader: w, Record: e.Cursor()}
			}
		}

	case "tool_batch":
		var v conversation.ToolBatchEvent
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if v.Agent != actor || len(v.Batch.Calls) == 0 || v.Batch.ContextRevision != uint64(len(p.histories[actor])) {
			return errors.New("invalid tool batch")
		}
	case "context_tokens":
		var v conversation.ContextTokensEvent
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if v.Agent != actor || v.Revision == 0 || v.Revision > uint64(len(p.histories[actor])) || v.Count < 0 {
			return errors.New("invalid context measurement")
		}
	case "commentary":
		var v conversation.CommentaryEvent
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		if v.Agent != actor {
			return errors.New("commentary agent mismatch")
		}
		if v.Output != nil {
			o, ok := p.outputs[*v.Output]
			if !ok || o.Status != agent.OutputComplete || v.Output.Agent != actor {
				return errors.New("commentary without completed output")
			}
		}
	case "diagnostic":
		var v conversation.DiagnosticEvent
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		switch v.Level {
		case "debug", "info", "warn", "error":
		default:
			return errors.New("invalid diagnostic level")
		}
	case "agent_exited":
		var v struct {
			Agent identity.ActorID `json:"agent"`
		}
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			return err
		}
		a, ok := p.agents[actor]
		if !ok || v.Agent != actor || !a.State.Terminal() {
			return errors.New("exit before terminal agent state")
		}

	default:
		return fmt.Errorf("unknown required record kind %q", e.Kind)
	}
	if e.Sequence == 1 && e.Kind != "session_started" {
		return errors.New("missing session start")
	}
	if commit != nil {
		commit()
	}
	if isFramed {
		p.contents[framed.Content.ID] = *framed.Content
	}
	p.facts[e.Kind] = append(p.facts[e.Kind], e.Cursor())
	p.cursor = e.Cursor()
	return nil
}
func (p *Projector) Output(id identity.OutputID) (OutputView, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	o, ok := p.outputs[id]
	if !ok {
		return OutputView{}, ErrNotFound
	}
	o.Through = p.cursor
	if o.HistoryPosition != nil {
		v := *o.HistoryPosition
		o.HistoryPosition = &v
	}
	if o.Error != nil {
		v := *o.Error
		o.Error = &v
	}
	return o, nil
}
func (p *Projector) Content(id identity.ContentID) (eventlog.ContentRef, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.contents[id]
	if !ok {
		return r, ErrNotFound
	}
	return r, nil
}
func (p *Projector) History(id identity.ActorID, before uint64, limit int) ([]HistoryView, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	h, ok := p.histories[id]
	if !ok {
		return nil, ErrNotFound
	}
	end := len(h)
	if before > 0 {
		if before > uint64(end)+1 {
			return nil, eventlog.ErrFuture
		}
		end = int(before - 1)
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("invalid history limit")
	}
	out := append([]HistoryView(nil), h[max(0, end-limit):end]...)
	for i := range out {
		if out[i].Output != nil {
			v := *out[i].Output
			out[i].Output = &v
		}
	}
	return out, nil
}
func (p *Projector) Agent(id identity.ActorID) (conversation.AgentInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a, ok := p.agents[id]
	if !ok {
		return a, ErrNotFound
	}
	return a, nil
}
func (p *Projector) Facts(kind string) []eventlog.Cursor {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]eventlog.Cursor(nil), p.facts[kind]...)
}

func (p *Projector) AgentInspection(id identity.ActorID) (conversation.AgentInspection, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	a, ok := p.agents[id]
	if !ok {
		return conversation.AgentInspection{}, ErrNotFound
	}
	u := p.usage[id]
	if u.Latest != nil {
		v := *u.Latest
		v.Usage = v.Usage.Clone()
		u.Latest = &v
	}
	var limit *int64
	if p.limits[id] != nil {
		v := *p.limits[id]
		limit = &v
	}
	return conversation.AgentInspection{AgentInfo: a, Usage: u, ContextRevision: uint64(len(p.histories[id])), OutputTokenLimit: limit}, nil
}
func (p *Projector) Agents() []conversation.AgentInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]conversation.AgentInfo, 0, len(p.agents))
	for _, a := range p.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *Projector) Receipt(id identity.MessageID) (message.Receipt, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.receipts[id]
	return r, ok
}

func (p *Projector) Work(id work.ID) (WorkView, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.workViews[id]
	if !ok {
		return v, ErrNotFound
	}
	v.Through = p.cursor
	return v, nil
}
