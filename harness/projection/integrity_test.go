package projection_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

// seeded is a projector holding a valid prefix: a started session, a root agent
// with one history entry, and an open output.
type seeded struct {
	t *testing.T
	p *projection.Projector
}

const (
	seedSession = "session"
	seedAgent   = identity.ActorID("root")
)

var seedOutput = identity.OutputID{Agent: seedAgent, Call: 1}

func (s *seeded) record(d eventlog.Data) eventlog.Record {
	return eventlog.Record{Schema: eventlog.SchemaVersion, Session: seedSession, Sequence: s.p.Cursor().Sequence + 1, Data: d}
}

// apply requires the record to be accepted.
func (s *seeded) apply(d eventlog.Data) {
	s.t.Helper()
	if err := s.p.Apply(s.record(d)); err != nil {
		s.t.Fatal(d.Kind, err)
	}
}

func (s *seeded) event(e conversation.Event) eventlog.Data {
	s.t.Helper()
	d, err := eventcodec.EncodeEvent(e)
	if err != nil {
		s.t.Fatal(err)
	}
	return d
}

// reject requires the record to be refused without advancing the projection.
func (s *seeded) reject(name string, d eventlog.Data) {
	s.t.Helper()
	before := s.p.Cursor()
	if err := s.p.Apply(s.record(d)); err == nil {
		s.t.Fatal("accepted", name)
	}
	if s.p.Cursor() != before {
		s.t.Fatal("rejected record advanced the cursor:", name)
	}
}

func newSeeded(t *testing.T) *seeded {
	t.Helper()
	s := &seeded{t: t, p: projection.New(seedSession)}
	s.apply(eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"` + seedSession + `"}`)})
	s.apply(s.event(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: seedAgent, Parent: message.User, State: agent.Idle, StateRevision: 1}}))
	s.apply(s.event(conversation.AgentRegistered{Registration: roster.Registration{AgentID: seedAgent, Parent: message.User, Role: roster.Root}}))
	s.apply(s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 1, Message: provider.Message{Role: "user"}}}))
	s.apply(s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputStarted{Output: seedOutput, ContextRevision: 1, StartedAt: time.Now()}}))
	return s
}

// The record envelope is validated before any payload is interpreted.
func TestProjectorRejectsMalformedEnvelope(t *testing.T) {
	s := newSeeded(t)
	valid := s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputDelta{Output: seedOutput, Offset: 0, Text: "ok"}})

	gap := s.record(valid)
	gap.Sequence += 1
	if err := s.p.Apply(gap); err == nil {
		t.Fatal("accepted a sequence gap")
	}
	stale := s.record(valid)
	stale.Sequence = 1
	if err := s.p.Apply(stale); err != nil {
		t.Fatal("a replayed record should be ignored, not rejected:", err)
	}
	unsupported := s.record(valid)
	unsupported.Schema = eventlog.SchemaVersion + 1000
	if err := s.p.Apply(unsupported); err == nil {
		t.Fatal("accepted an unsupported schema")
	}
	foreign := s.record(valid)
	foreign.Session = "other"
	if err := s.p.Apply(foreign); err == nil {
		t.Fatal("accepted a foreign session")
	}
	s.reject("invalid JSON payload", eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{`)})
	s.reject("unknown kind", eventlog.Data{Kind: "future_required_kind", Payload: valid.Payload})

	// Nothing may follow a terminal record.
	s.apply(eventlog.Data{Kind: "session_closed", Payload: json.RawMessage(`{"reason":"requested"}`)})
	s.reject("record after session terminal", valid)
}

// A session start is only valid as the first record, and later records may not
// claim to open the session again.
func TestProjectorRejectsMisplacedSessionRecords(t *testing.T) {
	p := projection.New(seedSession)
	first := eventlog.Record{Schema: eventlog.SchemaVersion, Session: seedSession, Sequence: 1, Data: eventlog.Data{Kind: "session_configured", Payload: json.RawMessage(`{}`)}}
	if err := p.Apply(first); err == nil {
		t.Fatal("accepted configuration before the session started")
	}
	mismatched := eventlog.Record{Schema: eventlog.SchemaVersion, Session: seedSession, Sequence: 1, Data: eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"other"}`)}}
	if err := p.Apply(mismatched); err == nil {
		t.Fatal("accepted a session start naming a different session")
	}
	s := newSeeded(t)
	s.reject("second session start", eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"` + seedSession + `"}`)})
}

// Agent lifecycle records must describe an agent the projection already knows,
// and must advance its state revision exactly one step at a time.
func TestProjectorRejectsInconsistentAgentRecords(t *testing.T) {
	s := newSeeded(t)
	s.reject("duplicate agent", s.event(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: seedAgent, Parent: message.User, State: agent.Idle, StateRevision: 1}}))
	s.reject("agent start at a non-initial revision", s.event(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "other", Parent: seedAgent, State: agent.Idle, StateRevision: 7}}))
	s.reject("state change for an unknown agent", s.event(conversation.AgentStateChanged{Agent: "ghost", State: agent.Running, Revision: 2}))
	s.reject("state revision gap", s.event(conversation.AgentStateChanged{Agent: seedAgent, State: agent.Running, Revision: 9}))
	s.reject("unknown agent state", s.event(conversation.AgentStateChanged{Agent: seedAgent, State: "teleporting", Revision: 2}))
	s.reject("duplicate registration", s.event(conversation.AgentRegistered{Registration: roster.Registration{AgentID: seedAgent, Parent: message.User, Role: roster.Root}}))
	s.reject("registration without a runtime agent", s.event(conversation.AgentRegistered{Registration: roster.Registration{AgentID: "ghost", Parent: seedAgent, Role: roster.Implementor}}))

	// A child may hold any creatable role, but not the root role.
	s.apply(s.event(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "child", Parent: seedAgent, State: agent.Idle, StateRevision: 1}}))
	s.reject("root role on a child agent", s.event(conversation.AgentRegistered{Registration: roster.Registration{AgentID: "child", Parent: seedAgent, Role: roster.Root}}))
	s.apply(s.event(conversation.AgentRegistered{Registration: roster.Registration{AgentID: "child", Parent: seedAgent, Role: roster.Implementor}}))

	// A valid state change is accepted and visible through the read model.
	s.apply(s.event(conversation.AgentStateChanged{Agent: seedAgent, State: agent.Running, Revision: 2}))
	a, err := s.p.Agent(seedAgent)
	if err != nil || a.State != agent.Running || a.StateRevision != 2 {
		t.Fatal(a, err)
	}
	if _, err := s.p.Agent("ghost"); err != projection.ErrNotFound {
		t.Fatal("unknown agent was not reported as missing", err)
	}
	agents := s.p.Agents()
	if len(agents) != 2 || agents[0].ID != "child" || agents[1].ID != seedAgent {
		t.Fatal("agent listing is not sorted or complete", agents)
	}
}

// Output records form a strict sequence: start, deltas at the running offset,
// then one finish consistent with the bytes and history recorded so far.
func TestProjectorRejectsInconsistentOutputRecords(t *testing.T) {
	s := newSeeded(t)
	s.reject("duplicate output start", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputStarted{Output: seedOutput, ContextRevision: 1, StartedAt: time.Now()}}))
	s.reject("output start without a start time", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputStarted{Output: identity.OutputID{Agent: seedAgent, Call: 2}, ContextRevision: 1}}))
	s.reject("output before agent start", s.event(conversation.AgentEvent{Agent: "ghost", Event: agent.OutputStarted{Output: identity.OutputID{Agent: "ghost", Call: 1}, ContextRevision: 0, StartedAt: time.Now()}}))
	s.reject("delta at the wrong offset", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputDelta{Output: seedOutput, Offset: 42, Text: "x"}}))
	s.reject("empty delta", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputDelta{Output: seedOutput, Offset: 0, Text: ""}}))
	s.reject("delta for an unknown output", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputDelta{Output: identity.OutputID{Agent: seedAgent, Call: 9}, Text: "x"}}))

	s.apply(s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputDelta{Output: seedOutput, Offset: 0, Text: "hello"}}))
	finish := func(f agent.OutputFinished) eventlog.Data {
		return s.event(conversation.AgentEvent{Agent: seedAgent, Event: f})
	}
	now := time.Now()
	s.reject("finish with the wrong byte count", finish(agent.OutputFinished{Output: seedOutput, Status: agent.OutputComplete, Bytes: 1, FinishedAt: now}))
	s.reject("completion without a history position", finish(agent.OutputFinished{Output: seedOutput, Status: agent.OutputComplete, Bytes: 5, FinishedAt: now}))
	s.reject("failure without an error", finish(agent.OutputFinished{Output: seedOutput, Status: agent.OutputFailed, Bytes: 5, FinishedAt: now}))

	// A completion must point at a history entry belonging to this output.
	s.apply(s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 2, Message: provider.Message{Role: "assistant"}}}))
	orphan := uint64(2)
	s.reject("completion citing another output's history", finish(agent.OutputFinished{Output: seedOutput, Status: agent.OutputComplete, Bytes: 5, HistoryPosition: &orphan, FinishedAt: now}))

	s.apply(s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 3, Message: provider.Message{Role: "assistant"}, Output: &seedOutput}}))
	owned := uint64(3)
	s.apply(finish(agent.OutputFinished{Output: seedOutput, Status: agent.OutputComplete, Bytes: 5, HistoryPosition: &owned, FinishedAt: now}))
	v, err := s.p.Output(seedOutput)
	if err != nil || v.Status != agent.OutputComplete || v.TextBytes != 5 {
		t.Fatal(v, err)
	}
	s.reject("delta after the output finished", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputDelta{Output: seedOutput, Offset: 5, Text: "more"}}))
}

// History is append-only at the next position, with a role the transcript
// understands.
func TestProjectorRejectsInconsistentHistoryRecords(t *testing.T) {
	s := newSeeded(t)
	s.reject("history position gap", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 9, Message: provider.Message{Role: "user"}}}))
	s.reject("history before agent start", s.event(conversation.AgentEvent{Agent: "ghost", Event: agent.HistoryAppended{Position: 1, Message: provider.Message{Role: "user"}}}))
	s.reject("unknown history role", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 2, Message: provider.Message{Role: "narrator"}}}))
	other := identity.OutputID{Agent: seedAgent, Call: 8}
	s.reject("history citing an unknown output", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 2, Message: provider.Message{Role: "assistant"}, Output: &other}}))
	s.reject("non-assistant history citing an output", s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.HistoryAppended{Position: 2, Message: provider.Message{Role: "user"}, Output: &seedOutput}}))
}

// Content chunks accumulate at a running offset under one identity.
func TestProjectorRejectsInconsistentContentChunks(t *testing.T) {
	s := newSeeded(t)
	chunk := func(id string, offset uint64, data string) eventlog.Data {
		body, err := json.Marshal(map[string]any{"id": id, "offset": offset, "data": []byte(data)})
		if err != nil {
			t.Fatal(err)
		}
		return eventlog.Data{Kind: "content_chunk", Payload: body}
	}
	s.reject("chunk without an identity", chunk("", 0, "body"))
	s.reject("empty chunk", chunk("content-1", 0, ""))
	s.reject("chunk at the wrong offset", chunk("content-1", 5, "body"))
	s.apply(chunk("content-1", 0, "body"))
	s.apply(chunk("content-1", 4, "more"))
	s.reject("chunk replaying a committed offset", chunk("content-1", 0, "again"))
}
