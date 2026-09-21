package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func progress(m *model, actor message.ActorID, body string) {
	m.observe(conversation.CommentaryEvent{Agent: actor, Content: body})
}

func activitySetup(t *testing.T) *model {
	t.Helper()
	m, _ := setup(t)
	m.resize(124, 38)
	m.entries = nil
	m.selectStream("root")
	for i := 1; i <= 3; i++ {
		progress(m, "root", fmt.Sprintf("update %d", i))
		addTool(m, "root", "Read file")
	}
	return m
}

func TestActivityKeepsEveryUpdateAndToolCall(t *testing.T) {
	m := activitySetup(t)
	m.resize(124, 60)
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "reply", From: "worker", To: "root", Kind: message.Reply, Content: "Ready for review"}})
	view := ansi.Strip(m.viewport.View())
	for _, text := range []string{"update 1", "update 2", "update 3", "Ready for review"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q: %s", text, view)
		}
	}
	if strings.Count(view, agentGlyph("root")+" Read file") != 3 {
		t.Fatalf("repeated tool calls were combined: %s", view)
	}
	m.selectStream("")
	m.selectStream("root")
	if !strings.Contains(m.viewport.View(), "update 1") {
		t.Fatal("switching streams hid older progress")
	}
}

func TestActivityPreservesHistoryAndFrozenDisplay(t *testing.T) {
	m := activitySetup(t)
	for i := 0; i < 30; i++ {
		m.addAttributed("Message", "root", fmt.Sprintf("history %d", i), false, "root")
	}
	m.viewport.GotoBottom()
	m.viewport.ScrollUp(8)
	before := m.viewport.View()
	progress(m, "root", "newest progress")
	if m.viewport.View() != before {
		t.Fatal("new output displaced history")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	progress(m, "root", "another update")
	if m.View() != frozen {
		t.Fatal("output changed frozen display")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m.viewport.GotoTop()
	if !strings.Contains(m.viewport.View(), "update 1") {
		t.Fatal("offscreen activity was hidden")
	}
}

func TestIndividualToolCallsPreserveOrderAndWrap(t *testing.T) {
	m, _ := setup(t)
	m.resize(100, 60)
	m.entries = nil
	m.selectStream("")
	for _, call := range []struct {
		actor message.ActorID
		body  string
	}{
		{"worker", "Read file"}, {"root", "Shell"}, {"worker", "Read file"},
		{"root", "A long custom tool name " + strings.Repeat("extended ", 12) + "visible ending"},
	} {
		addTool(m, call.actor, call.body)
	}
	view := ansi.Strip(m.viewport.View())
	first, middle, last := strings.Index(view, agentGlyph("worker")+" Read file"), strings.Index(view, agentGlyph("root")+" Shell"), strings.LastIndex(view, agentGlyph("worker")+" Read file")
	if first < 0 || middle <= first || last <= middle || !strings.Contains(view, "visible ending") {
		t.Fatalf("tool calls merged, reordered or truncated: %s", view)
	}
	assertFits(t, view, m.viewport.Width, 0)
}
