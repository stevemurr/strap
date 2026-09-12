package tui

import (
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"testing"
)

func TestStreamUpdatesOneRowAndReplyReconciles(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	id := identity.OutputID{Agent: "root", Call: 1}
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputStarted{Output: id}})
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: id, Text: "first "}})
	if len(m.entries) != 1 || m.entries[0].body != "first " {
		t.Fatal(m.entries)
	}
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: id, Offset: 6, Text: "second"}})
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputFinished{Output: id, Status: agent.OutputComplete, Bytes: 12}})
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "reply", From: "root", To: message.User, Kind: message.Reply, Content: "first second", Output: &id}})
	if len(m.entries) != 1 || m.entries[0].body != "first second" || m.entries[0].message != "reply" {
		t.Fatal(m.entries)
	}
}
func TestReplayedUserInputAppearsAndReconcilesOptimisticRow(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	e := conversation.MessageEvent{Message: message.Message{ID: "input", From: message.User, To: "root", Kind: message.Instruction, Content: "accepted"}}
	m.observe(e)
	m.observe(e)
	if len(m.entries) != 1 || m.entries[0].label != "You" || m.entries[0].body != "accepted" {
		t.Fatal(m.entries)
	}
}
