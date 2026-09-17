package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func wheel(button tea.MouseButton) tea.MouseMsg {
	return tea.MouseMsg{Button: button, Action: tea.MouseActionPress}
}

func TestMouseHistoryPreservesPositionAndResumesFollowing(t *testing.T) {
	m, _ := setup(t)
	for i := 0; i < 30; i++ {
		m.add("Strap", fmt.Sprintf("message %d", i), true)
	}
	m.input.SetValue("unfinished draft")
	m.Update(wheel(tea.MouseButtonWheelUp))
	if m.viewport.AtBottom() || !strings.Contains(m.footer(), "History") {
		t.Fatal("wheel did not scroll into history")
	}
	offset, view := m.viewport.YOffset, m.viewport.View()
	m.Update(received{event: conversation.MessageEvent{Message: message.Message{
		From: "root", To: message.User, Kind: message.Reply, Content: "new reply",
	}}})
	if m.viewport.YOffset != offset || m.viewport.View() != view || m.input.Value() != "unfinished draft" {
		t.Fatal("incoming output disturbed history or draft")
	}
	m.Update(wheel(tea.MouseButtonWheelDown))
	if m.viewport.YOffset <= offset {
		t.Fatal("wheel down did not advance history")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m.add("Strap", "latest reply", false)
	if !m.viewport.AtBottom() || !strings.Contains(m.View(), "latest reply") || strings.Contains(m.footer(), "History") {
		t.Fatal("returning to latest did not resume following")
	}
}

func TestCopyModeReleasesAndRestoresMouseCapture(t *testing.T) {
	m, _ := setup(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.DisableMouse()) {
		t.Fatal("copy mode did not release mouse capture")
	}
	frozen := m.View()
	m.Update(wheel(tea.MouseButtonWheelUp))
	if m.View() != frozen {
		t.Fatal("mouse changed frozen display")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.EnableMouseAllMotion()) {
		t.Fatal("leaving copy mode did not restore mouse capture")
	}
}

func TestMouseScrollsAgentTranscriptAndLoadsOlderMessages(t *testing.T) {
	m, s := transcriptSetup(t)
	enter(m, "/transcript agent-7")
	mainOffset, offset := m.viewport.YOffset, m.transcript.viewport.YOffset
	m.Update(wheel(tea.MouseButtonWheelUp))
	if m.transcript.viewport.YOffset >= offset || m.viewport.YOffset != mainOffset {
		t.Fatal("mouse did not scroll only the selected transcript")
	}
	m.transcript.viewport.GotoTop()
	m.Update(wheel(tea.MouseButtonWheelUp))
	if len(s.queries) != 2 || s.queries[1].Before != 6 || len(m.transcript.inspection.Transcript.Entries) != 25 {
		t.Fatal("mouse did not load older messages")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.DisableMouse()) {
		t.Fatal("transcript copy mode did not release mouse capture")
	}
	offset = m.transcript.viewport.YOffset
	m.Update(wheel(tea.MouseButtonWheelDown))
	if m.transcript.viewport.YOffset != offset {
		t.Fatal("mouse scrolled transcript during selection")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.transcript != nil || cmd == nil || reflect.TypeOf(cmd()) != reflect.TypeOf(tea.EnableMouseAllMotion()) {
		t.Fatal("leaving transcript copy mode did not restore mouse capture")
	}
}
