package projection_test

import (
	"encoding/json"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"testing"
	"time"
)

func TestInvalidRecordLeavesProjectionAndCursorUnchanged(t *testing.T) {
	p := projection.New("session")
	seq := uint64(0)
	apply := func(d eventlog.Data) {
		t.Helper()
		seq++
		if err := p.Apply(eventlog.Record{Schema: eventlog.SchemaVersion, Session: "session", Sequence: seq, Data: d}); err != nil {
			t.Fatal(err)
		}
	}
	apply(eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)})
	for _, e := range []conversation.Event{
		conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "agent", Parent: "user", State: agent.Idle, StateRevision: 1}},
		conversation.AgentEvent{Agent: "agent", Event: agent.HistoryAppended{Position: 1, Message: provider.Message{Role: "system"}}},
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputStarted{Output: identity.OutputID{Agent: "agent", Call: 1}, ContextRevision: 1, StartedAt: time.Now()}},
	} {
		d, err := eventcodec.EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		apply(d)
	}
	cursor := p.Cursor()
	id := identity.OutputID{Agent: "agent", Call: 1}
	bad, _ := eventcodec.EncodeEvent(conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: id, Offset: 99, Text: "bad"}})
	e := eventlog.Record{Schema: eventlog.SchemaVersion, Session: "session", Sequence: cursor.Sequence + 1, Data: bad}
	if err := p.Apply(e); err == nil {
		t.Fatal("accepted invalid offset")
	}
	if p.Cursor() != cursor {
		t.Fatal("advanced cursor")
	}
	v, _ := p.Output(id)
	if v.TextBytes != 0 {
		t.Fatal(v)
	}
	e.Kind = "future_required_kind"
	if err := p.Apply(e); err == nil {
		t.Fatal("accepted unknown kind")
	}
	e.Session = "other"
	if err := p.Apply(e); err == nil {
		t.Fatal("accepted foreign session")
	}
	e.Session = "session"
	e.Sequence = cursor.Sequence
	if err := p.Apply(e); err != nil {
		t.Fatal("duplicate prefix was not idempotent", err)
	}
}
