package eventcodec_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

var codecOutput = identity.OutputID{Agent: "agent", Call: 1}

// roundTrip encodes an event, replays it as a stored record, and returns what a
// reader would see.
func roundTrip(t *testing.T, e conversation.Event) conversation.Event {
	t.Helper()
	d, err := eventcodec.EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := eventcodec.DecodeEvent(eventlog.Event{Schema: eventlog.SchemaVersion, Session: "s", Sequence: 1, Data: d})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Every event the host publishes survives a trip through the log unchanged.
func TestEventsSurviveEncodeAndDecode(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	in, out := int64(3), int64(4)
	position := uint64(1)
	for _, e := range []conversation.Event{
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputStarted{Output: codecOutput, ContextRevision: 1, StartedAt: now}},
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: codecOutput, Channel: provider.ChannelContent, Text: "hi"}},
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: codecOutput, Channel: provider.ChannelReasoning, Text: "because"}},
		conversation.AgentEvent{Agent: "agent", Event: agent.HistoryAppended{Position: 1, Message: provider.Message{Role: "user"}}},
		conversation.AgentEvent{Agent: "agent", Event: agent.OutputFinished{Output: codecOutput, Status: agent.OutputComplete, Bytes: 2, HistoryPosition: &position, FinishedAt: now}},
		conversation.ContextTokensEvent{Agent: "agent", Revision: 1, Count: 5},
		conversation.DiagnosticEvent{Level: "info", Message: "noted"},
		conversation.MessageEvent{Message: message.Message{ID: "m-1", From: message.User, To: "agent", Kind: message.Instruction, Content: "hi"}},
		conversation.CommentaryEvent{Agent: "agent", Content: "thinking"},
		conversation.AckEvent{Receipt: message.Receipt{MessageID: "m-1", Recipient: "agent", Status: message.Queued}},
		conversation.AgentRegistered{Registration: roster.Registration{AgentID: "agent", Parent: message.User, Role: roster.Root}},
		conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "agent", Parent: message.User, State: agent.Idle, StateRevision: 1}},
		conversation.AgentStateChanged{Agent: "agent", State: agent.Running, Revision: 2},
		conversation.ToolEvent{Agent: "agent", Activity: agent.ToolActivity{InvocationID: "agent/tool-1", Call: provider.ToolCall{ID: "c", Name: "shell", Arguments: json.RawMessage(`{"input":{}}`)}, StartedAt: now}},
		conversation.WorkEvent{Event: work.Event{ID: "e-1", Kind: work.WorkAssigned, Actor: "agent"}},
		conversation.ToolBatchEvent{Agent: "agent", Batch: agent.ToolBatch{Calls: []string{"c"}, ContextRevision: 1}},
		conversation.UsageEvent{Agent: "agent", Observation: agent.UsageObservation{Call: 1, ContextRevision: 1, Usage: &provider.Usage{InputTokens: &in, OutputTokens: &out}}},
	} {
		if got := roundTrip(t, e); reflect.TypeOf(got) != reflect.TypeOf(e) {
			t.Fatalf("%T decoded as %T", e, got)
		}
	}
}

// An agent's failure reason is preserved as a coded problem, distinguishing a
// timeout and a cancellation from an ordinary generation failure.
func TestOutputFailureCodesAreClassified(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{errors.New("model refused"), "generation_failed"},
	} {
		d, err := eventcodec.EncodeEvent(conversation.AgentEvent{Agent: "agent", Event: agent.OutputFinished{Output: codecOutput, Status: agent.OutputFailed, FinishedAt: now, Err: tc.err}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(d.Payload), `"code":"`+tc.code+`"`) {
			t.Fatal("wrong failure code for", tc.err, string(d.Payload))
		}
		got := roundTrip(t, conversation.AgentEvent{Agent: "agent", Event: agent.OutputFinished{Output: codecOutput, Status: agent.OutputFailed, FinishedAt: now, Err: tc.err}})
		finished := got.(conversation.AgentEvent).Event.(agent.OutputFinished)
		if finished.Err == nil || !strings.Contains(finished.Err.Error(), tc.err.Error()) {
			t.Fatal("failure reason was lost", finished.Err)
		}
	}
	// An agent exit carries its reason across the log, and a clean exit carries none.
	exited := roundTrip(t, conversation.AgentExited{Agent: "agent", Err: errors.New("stopped early")})
	if e := exited.(conversation.AgentExited); e.Err == nil || e.Err.Error() != "stopped early" {
		t.Fatal(e.Err)
	}
	if e := roundTrip(t, conversation.AgentExited{Agent: "agent"}).(conversation.AgentExited); e.Err != nil {
		t.Fatal("invented an exit reason", e.Err)
	}
}

// The codec refuses what it cannot represent rather than writing a record a
// reader would misinterpret.
func TestEncodeRejectsUnknownEvents(t *testing.T) {
	if _, err := eventcodec.EncodeEvent(unknownEvent{}); err == nil {
		t.Fatal("encoded an event the codec does not know")
	}
	if _, err := eventcodec.EncodeEvent(conversation.AgentEvent{Agent: "agent", Event: unknownFact{}}); err == nil {
		t.Fatal("encoded an agent fact the codec does not know")
	}
	if _, err := eventcodec.EncodeEvent(conversation.AgentEvent{Agent: "agent", Event: agent.OutputDelta{Output: codecOutput, Channel: "telepathy", Text: "x"}}); err == nil {
		t.Fatal("encoded an unknown output channel")
	}
}

// Embedding a known event borrows its interface without being that type, which
// is what an event from a newer writer looks like to this codec.
type unknownEvent struct{ conversation.AckEvent }

type unknownFact struct{ agent.OutputStarted }

// Reading refuses records it cannot interpret, and reports control records as
// carrying no domain event rather than as an error.
func TestDecodeRejectsUnreadableRecords(t *testing.T) {
	record := func(schema int, kind, payload string) eventlog.Event {
		return eventlog.Event{Schema: schema, Session: "s", Sequence: 1, Data: eventlog.Data{Kind: kind, Agent: "agent", Payload: json.RawMessage(payload)}}
	}
	for _, tc := range []struct {
		name string
		e    eventlog.Event
	}{
		{"unsupported schema", record(99, "message", `{}`)},
		{"unknown kind", record(eventlog.SchemaVersion, "telepathy", `{}`)},
		{"omitted payload", record(eventlog.SchemaVersion, "omitted", `{"reason":"too large"}`)},
		{"malformed message", record(eventlog.SchemaVersion, "message", `{"message":5}`)},
		{"malformed agent fact", record(eventlog.SchemaVersion, "output_started", `{"output":5}`)},
		{"malformed delta", record(eventlog.SchemaVersion, "output_delta", `{"output":5}`)},
		{"malformed history", record(eventlog.SchemaVersion, "history_appended", `{"position":"one"}`)},
		{"malformed finish", record(eventlog.SchemaVersion, "output_finished", `{"output":5}`)},
		{"malformed exit", record(eventlog.SchemaVersion, "agent_exited", `{"agent":5}`)},
		{"reasoning under a content-only schema", record(2, "output_delta", `{"output":{"agent":"agent","call":1},"channel":"reasoning","text":"x"}`)},
		{"invalid channel", record(eventlog.SchemaVersion, "output_delta", `{"output":{"agent":"agent","call":1},"channel":"telepathy","text":"x"}`)},
	} {
		if _, err := eventcodec.DecodeEvent(tc.e); err == nil {
			t.Fatal("decoded an unreadable record:", tc.name)
		}
	}
	for _, kind := range []string{"content_chunk", "session_closed", "session_started", "session_configured"} {
		v, err := eventcodec.DecodeEvent(record(eventlog.SchemaVersion, kind, `{}`))
		if err != nil || v != nil {
			t.Fatal("control record was not reported as eventless:", kind, v, err)
		}
	}
}

// A legacy content-only record still reads, with its channel filled in.
func TestOutputChannelInterpretsLegacySchemas(t *testing.T) {
	if _, err := eventcodec.OutputChannel(99, provider.ChannelContent); err == nil {
		t.Fatal("accepted an unsupported schema")
	}
	c, err := eventcodec.OutputChannel(2, "")
	if err != nil || c != provider.ChannelContent {
		t.Fatal(c, err)
	}
	if _, err := eventcodec.OutputChannel(2, provider.ChannelReasoning); err == nil {
		t.Fatal("accepted reasoning under a content-only schema")
	}
	if _, err := eventcodec.OutputChannel(eventlog.SchemaVersion, "telepathy"); err == nil {
		t.Fatal("accepted an unknown channel")
	}
	if c, err := eventcodec.OutputChannel(eventlog.SchemaVersion, provider.ChannelReasoning); err != nil || c != provider.ChannelReasoning {
		t.Fatal(c, err)
	}
}
