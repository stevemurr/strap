package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func typeText(m *model, text string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func TestSlashCompletionFiltersAndAcceptsWithoutSubmitting(t *testing.T) {
	m, s := setup(t)
	typeText(m, "/")
	if got := m.completionMatches(); len(got) != len(slashCommands) {
		t.Fatal(got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "› /activity") {
		t.Fatal(m.View())
	}
	typeText(m, "tr")
	if got := m.completionMatches(); len(got) != 1 || got[0].name != "/transcript" {
		t.Fatal(got)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "/transcript " || len(m.completionMatches()) != 0 || len(s.sent) != 0 || m.transcript != nil {
		t.Fatal("Tab submitted the command or failed to complete it")
	}
	typeText(m, "agent-7")
	if m.input.Value() != "/transcript agent-7" || len(m.completionMatches()) != 0 {
		t.Fatal("argument input lost", m.input.Value())
	}
	// Exact commands retain the existing Enter behavior.
	enter(m, "/inspect agent-7")
	if s.managed != "inspect:agent-7" || len(s.sent) != 0 {
		t.Fatal("slash command reached model or did not run")
	}
}

func TestSlashSelectionWrapsAndScrollsToSelectedCommand(t *testing.T) {
	m, _ := setup(t)
	typeText(m, "/")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.completion.selected != len(slashCommands)-1 || !strings.Contains(ansi.Strip(m.View()), "› /transcript") {
		t.Fatal("last suggestion not selected or visible", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.completion.selected != 0 {
		t.Fatal("selection did not wrap")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "/agents " {
		t.Fatal(m.input.Value())
	}
}

func TestEnterCompletesPartialQuitBeforeExecuting(t *testing.T) {
	m, _ := setup(t)
	typeText(m, "/q")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.quitting || m.input.Value() != "/quit " {
		t.Fatal("partial completion executed quit")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.quitting || cmd == nil {
		t.Fatal("confirmed quit did not execute")
	}
}

func TestEscapeDismissesUntilEditedAndHistoryStillWorks(t *testing.T) {
	m, _ := setup(t)
	enter(m, "older message")
	typeText(m, "/")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.completionMatches()) != 0 || m.input.Value() != "/" {
		t.Fatal("dismiss changed draft")
	}
	m.Update(received{event: conversation.MessageEvent{Message: message.Message{From: "root", To: message.User, Kind: message.Reply, Content: "reply"}}})
	if len(m.completionMatches()) != 0 {
		t.Fatal("incoming output reopened dismissed menu")
	}
	typeText(m, "h")
	if len(m.completionMatches()) != 1 {
		t.Fatal("editing did not restore suggestions")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "older message" {
		t.Fatal("history no longer works")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.input.Value() != "/h" {
		t.Fatal("history lost draft")
	}
}

func TestCompletionIgnoresOrdinaryTextArgumentsAndMidCommandEdits(t *testing.T) {
	for _, text := range []string{"hello /help", "/unknown", "/inspect ", "/inspect agent-1", "/help\t"} {
		m, _ := setup(t)
		typeText(m, text)
		if len(m.completionMatches()) != 0 {
			t.Fatalf("suggestions for %q", text)
		}
	}
	m, _ := setup(t)
	typeText(m, "/inspect")
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if len(m.completionMatches()) != 0 {
		t.Fatal("completion interferes with mid-command edit")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if len(m.completionMatches()) != 1 {
		t.Fatal("end of command did not restore suggestions")
	}
}

func TestCompletionPreservesScrollFreezeAndTerminalBounds(t *testing.T) {
	m, _ := setup(t)
	for i := range 30 {
		m.add("Strap", fmt.Sprintf("message %d", i), true)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	offset := m.viewport.YOffset
	typeText(m, "/")
	if m.viewport.YOffset != offset {
		t.Fatal("suggestions scrolled history")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.View() != frozen || m.input.Value() != "/" {
		t.Fatal("completion changed frozen view")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	for _, size := range []tea.WindowSizeMsg{
		{Width: 80, Height: 24}, {Width: 25, Height: 12}, {Width: 15, Height: 8}, {Width: 1, Height: 1}, {Width: 80, Height: 24},
	} {
		m.Update(size)
		view := m.View()
		if lines := strings.Split(view, "\n"); len(lines) > size.Height {
			t.Fatalf("menu exceeds height %d: %s", size.Height, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > size.Width {
				t.Fatalf("menu exceeds width %d: %q", size.Width, line)
			}
		}
		if m.input.Value() != "/" {
			t.Fatal("resize lost draft")
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewport.Height != m.height-6-m.streamChrome()-m.stackBarHeight() {
		t.Fatal("dismiss did not restore transcript height")
	}
}
