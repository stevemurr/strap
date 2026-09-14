package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func TestCommentarySeparatesToolGroupsAndKeepsAgentsWorking(t *testing.T) {
	for _, actor := range []message.ActorID{"root", "worker"} {
		t.Run(string(actor), func(t *testing.T) {
			m, _ := setup(t)
			m.entries = nil
			m.observe(conversation.AgentStateChanged{Agent: actor, State: agent.Running})
			started := m.busySince
			addTool(m, actor, "read_file")
			m.Update(received{event: conversation.CommentaryEvent{Agent: actor, Content: "This is a **DAW** in your browser."}})
			label := "Message"
			if actor == "root" {
				label = "Strap"
			}
			e := &m.entries[len(m.entries)-1]
			if e.label != label || e.meta != string(actor)+" · progress" {
				t.Fatalf("incorrect attribution: %+v", e)
			}
			if body := ansi.Strip(m.renderBody(e)); !strings.Contains(body, "DAW") || strings.Contains(body, "**") {
				t.Fatal("commentary did not render Markdown", body)
			}
			addTool(m, actor, "read_file")
			view := ansi.Strip(m.viewport.View())
			if strings.Count(view, "▸") != 2 || strings.Index(view, "DAW") < strings.Index(view, "▸") || strings.Index(view, "DAW") > strings.LastIndex(view, "▸") {
				t.Fatal("commentary did not separate tool groups", view)
			}
			if !m.working[actor] || m.states[actor] != agent.Running || m.busySince != started || len(m.pending) != 0 {
				t.Fatal("commentary changed activity or delivery state")
			}
		})
	}
}

func TestCommentaryPreservesScrollAndSelection(t *testing.T) {
	m, _ := setup(t)
	for i := range 30 {
		m.add("Strap", fmt.Sprintf("message %d", i), true)
	}
	m.input.SetValue("unfinished draft")
	m.Update(wheel(tea.MouseButtonWheelUp))
	offset, view := m.viewport.YOffset, m.viewport.View()
	m.Update(received{event: conversation.CommentaryEvent{Agent: "root", Content: "Checking the audio engine."}})
	if m.viewport.AtBottom() || m.viewport.YOffset != offset || m.viewport.View() != view || m.input.Value() != "unfinished draft" {
		t.Fatal("commentary disturbed history or draft")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(received{event: conversation.CommentaryEvent{Agent: "worker", Content: "Found the timeline."}})
	if m.View() != frozen {
		t.Fatal("commentary changed frozen selection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(m.View(), "Found the timeline.") {
		t.Fatal("commentary lost after leaving selection")
	}
}
