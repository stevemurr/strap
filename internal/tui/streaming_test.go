package tui

import (
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"strings"
	"testing"
)

func TestReasoningOmissionKeepsRecordedDataAndFailedPartialText(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	id := identity.OutputID{Agent: "root", Call: 1}
	observe := func(e agent.Event) { m.Update(received{event: conversation.AgentEvent{Agent: "root", Event: e}}) }
	observe(agent.OutputStarted{Output: id})
	observe(agent.OutputDelta{Output: id, Channel: provider.ChannelReasoning, Text: "private reasoning"})
	if strings.Contains(ansi.Strip(m.View()), "private reasoning") || m.entries[0].body != "" {
		t.Fatal(m.View())
	}
	// Ctrl+T only changes tool output, including while reasoning is streaming.
	m.input.SetValue("draft")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if strings.Contains(ansi.Strip(m.View()), "private reasoning") || m.input.Value() != "draft" {
		t.Fatal(m.View())
	}
	observe(agent.OutputDelta{Output: id, Channel: provider.ChannelContent, Text: "partial answer"})
	observe(agent.OutputDelta{Output: id, Channel: provider.ChannelReasoning, Offset: 17, Text: " continues"})
	observe(agent.OutputFinished{Output: id, Status: agent.OutputFailed, Err: errors.New("length limit")})
	view := ansi.Strip(m.View())
	if strings.Contains(view, "private reasoning") || m.entries[0].reasoning != "private reasoning continues" || !strings.Contains(view, "partial answer") || !strings.Contains(view, "failed") || len(m.entries) != 1 {
		t.Fatal(view)
	}
	if m.entries[0].body != "partial answer" {
		t.Fatal("reasoning mixed into answer")
	}
}

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
