package projection_test

import (
	"encoding/json"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/roster"
	"testing"
)

func TestRegistrationBoundaryAndLegacyUnknownRoles(t *testing.T) {
	for _, schema := range []int{2, 3, 4, 5} {
		t.Run(string(rune('0'+schema)), func(t *testing.T) {
			p := projection.New("session")
			if e := p.Apply(eventlog.Record{Session: "session", Sequence: 1, Schema: schema, Data: eventlog.Data{Kind: "session_started", Payload: json.RawMessage(`{"id":"session"}`)}}); e != nil {
				t.Fatal(e)
			}
			start, _ := eventcodec.EncodeEvent(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "root", Parent: "user", State: agent.Idle, StateRevision: 1}})
			if e := p.Apply(eventlog.Record{Session: "session", Sequence: 2, Schema: schema, Data: start}); e != nil {
				t.Fatal(e)
			}
			base, _ := p.AgentInspection("root")
			before := p.Enrich(base, nil)
			if before.Role != roster.Unknown || before.Registered || len(before.EligibleWorkKinds) != 0 {
				t.Fatal("inferred legacy role", before)
			}
			fact := conversation.AgentRegistered{Registration: roster.Registration{AgentID: "root", Parent: "user", Role: roster.Root}}
			data, _ := eventcodec.EncodeEvent(fact)
			record := eventlog.Record{Session: "session", Sequence: 3, Schema: 4, Data: data}
			decoded, e := eventcodec.DecodeEvent(record)
			if e != nil || decoded != fact {
				t.Fatal(decoded, e)
			}
			if e = p.Apply(record); e != nil {
				t.Fatal(e)
			}
			after := p.Enrich(base, nil)
			if after.Role != roster.Root || !after.Registered {
				t.Fatal(after)
			}
			record.Sequence++
			if e = p.Apply(record); e == nil {
				t.Fatal("duplicate registration accepted")
			}
			if p.Cursor().Sequence != 3 {
				t.Fatal("invalid registration advanced projection")
			}
		})
	}
}
