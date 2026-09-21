package eventcodec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/jsonstream"
	"hash"
	"sync"
	"time"
	"unicode/utf8"
)

// Publisher serializes encoding, framing and publication. Its mutex is not a
// runtime state lock; cancellation and Log.Fail do not depend on it.
type Publisher struct {
	mu         sync.Mutex
	log        *eventlog.Log
	chunkBytes int
	producers  chan struct{}
	deltaBytes int
}

func NewPublisher(log *eventlog.Log, queueBytes int) *Publisher {
	return &Publisher{log: log, chunkBytes: min(64<<10, (queueBytes-2048)/2), producers: make(chan struct{}, 64), deltaBytes: (min(queueBytes, 64<<10) - 1024) / 6}
}
func (p *Publisher) Publish(ctx context.Context, e conversation.Event) error {
	select {
	case p.producers <- struct{}{}:
		defer func() { <-p.producers }()
	default:
		return eventlog.ErrQuota
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := e.(conversation.AgentEvent); ok {
		if delta, ok := v.Event.(agent.OutputDelta); ok {
			return p.publishDelta(ctx, v.Agent, delta)
		}
	}
	d, body, err := describe(e)
	if err != nil {
		return err
	}
	return p.publishValue(ctx, d, body, func() (json.RawMessage, error) { return control(e) })
}

// PublishConfiguration frames construction metadata with the same record budget.
func (p *Publisher) PublishConfiguration(ctx context.Context, value any) error {
	select {
	case p.producers <- struct{}{}:
		defer func() { <-p.producers }()
	default:
		return eventlog.ErrQuota
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.publishValue(ctx, eventlog.Data{Kind: "session_configured"}, value, func() (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
}
func (p *Publisher) publishValue(ctx context.Context, d eventlog.Data, body any, metadata func() (json.RawMessage, error)) error {
	d.Time = time.Now().UTC()
	w := &framer{ctx: ctx, log: p.log, limit: p.chunkBytes, sum: sha256.New()}
	if err := jsonstream.Encode(w, body); err != nil {
		return err
	}
	if w.ref.ID == "" {
		d.Payload = w.buffer
	} else {
		if err := w.flush(); err != nil {
			return err
		}
		w.ref.SHA256 = hex.EncodeToString(w.sum.Sum(nil))
		control, err := metadata()
		if err != nil {
			return err
		}
		d.Payload, err = json.Marshal(record.Framed{Content: &w.ref, Control: control})
		if err != nil {
			return err
		}
	}
	_, err := p.log.PublishContext(ctx, d)
	return err
}

type framer struct {
	ctx    context.Context
	log    *eventlog.Log
	limit  int
	buffer []byte
	ref    eventlog.ContentRef
	sum    hash.Hash
}

func (w *framer) Write(b []byte) (int, error) {
	total := len(b)
	for len(b) > 0 {
		if len(w.buffer) == w.limit {
			if err := w.flush(); err != nil {
				return total - len(b), err
			}
		}
		n := min(len(b), w.limit-len(w.buffer))
		w.buffer = append(w.buffer, b[:n]...)
		b = b[n:]
	}
	return total, nil
}
func (w *framer) flush() error {
	if len(w.buffer) == 0 {
		return nil
	}
	if w.limit < 1 {
		return errors.New("invalid framing budget")
	}
	if w.ref.ID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		w.ref.ID = identity.ContentID(hex.EncodeToString(id[:]))
	}
	raw, err := json.Marshal(record.ContentChunk{ID: w.ref.ID, Offset: w.ref.Bytes, Data: w.buffer})
	if err != nil {
		return err
	}
	cursor, err := w.log.PublishContext(w.ctx, eventlog.Data{Kind: "content_chunk", Payload: raw})
	if err != nil {
		return err
	}
	if w.ref.Bytes == 0 {
		w.ref.First = cursor
	}
	w.ref.Last = cursor
	w.ref.Bytes += uint64(len(w.buffer))
	w.sum.Write(w.buffer)
	w.buffer = w.buffer[:0]
	return nil
}
func control(e conversation.Event) (json.RawMessage, error) {
	// Only bounded identity/status fields are needed to reduce framed records.
	var v any
	switch e := e.(type) {
	case conversation.AgentExited:
		v = exitedRecord{Agent: e.Agent, Error: "agent exited; details in content"}
	case conversation.ContextTokensEvent:
		e.Error = "token count unavailable; full details in content"
		v = e
	case conversation.AgentRegistered:
		v = e
	case conversation.AgentStarted:
		v = conversation.AgentStarted{Agent: e.Agent, OutputTokenLimit: e.OutputTokenLimit}
	case conversation.AgentEvent:
		switch x := e.Event.(type) {
		case agent.Yielded:
			v = x
		case agent.InboxDisposition:
			v = x
		case agent.OutputStarted:
			v = x
		case agent.HistoryAppended:
			v = struct {
				Position uint64             `json:"position"`
				Output   *identity.OutputID `json:"output,omitempty"`
				Role     string             `json:"role"`
			}{x.Position, x.Output, x.Message.Role}
		case agent.OutputFinished:
			y := x
			if y.Err != nil {
				y.Err = errors.New("generation failed; full details in content")
			}
			_, v, _ = describeAgent(conversation.AgentEvent{Agent: e.Agent, Event: y})
		default:
			return nil, errors.New("output control record exceeds budget")
		}
	case conversation.MessageEvent:
		m := e.Message
		m.Content = ""
		m.Work = nil
		m.Event = nil
		v = conversation.MessageEvent{Message: m}
	case conversation.ToolEvent:
		v = record.ToolControl{Execution: e.Activity.Result.Execution, Invocation: e.Activity.InvocationID, FinishedAt: e.Activity.FinishedAt, Name: e.Activity.Call.Name}
	case conversation.WorkEvent:
		v = record.DescribeWork(e.Event)
	case conversation.CommentaryEvent:
		v = conversation.CommentaryEvent{Agent: e.Agent, Output: e.Output}
	case conversation.DiagnosticEvent:
		v = conversation.DiagnosticEvent{Level: e.Level, Message: "details in content"}
	default:
		return nil, errors.New("control fields exceed framing budget")
	}
	return json.Marshal(v)
}

// Text deltas stay inline for prefix paging; escape expansion is included in the
// budget. Splitting preserves the agent's byte offsets and storage acknowledgment.
func (p *Publisher) publishDelta(ctx context.Context, actor identity.ActorID, d agent.OutputDelta) error {
	if d.Text == "" || !utf8.ValidString(d.Text) {
		return errors.New("invalid output delta")
	}
	for len(d.Text) > 0 {
		n := min(len(d.Text), p.deltaBytes)
		for n < len(d.Text) && !utf8.RuneStart(d.Text[n]) {
			n--
		}
		part := d
		part.Text = d.Text[:n]
		data, err := EncodeEvent(conversation.AgentEvent{Agent: actor, Event: part})
		if err != nil {
			return err
		}
		if _, err = p.log.PublishContext(ctx, data); err != nil {
			return err
		}
		d.Text = d.Text[n:]
		d.Offset += uint64(n)
	}
	return nil
}
