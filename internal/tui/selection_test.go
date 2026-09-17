package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func mouseAt(action tea.MouseAction, button tea.MouseButton, x, y int) tea.MouseMsg {
	return tea.MouseMsg{Action: action, Button: button, X: x, Y: y}
}

func screenLocation(t *testing.T, view, fragment string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(view), "\n") {
		if x := strings.Index(line, fragment); x >= 0 {
			return ansi.StringWidth(line[:x]), y
		}
	}
	t.Fatalf("%q not visible: %s", fragment, view)
	return 0, 0
}

func TestMouseDragCopiesDisplayedTextOnRelease(t *testing.T) {
	m, _ := setup(t)
	m.add("Strap", "**hello** world", false)
	x, y := screenLocation(t, m.View(), "hello")
	var copied []string
	m.copyText = func(text string) error { copied = append(copied, text); return nil }
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonLeft, x+4, y))
	if m.mouseSelection == nil || m.mouseSelection.text() != "hello" || len(copied) != 0 {
		t.Fatal("drag did not select text or copied before release")
	}
	if m.View() == strings.Join(m.mouseSelection.lines, "\n") {
		t.Fatal("selection is not highlighted")
	}
	_, cmd := m.Update(mouseAt(tea.MouseActionRelease, tea.MouseButtonLeft, x+4, y))
	if cmd == nil || len(copied) != 0 {
		t.Fatal("clipboard work was not asynchronous")
	}
	m.Update(cmd())
	if len(copied) != 1 || copied[0] != "hello" || !strings.Contains(m.View(), "Copied to clipboard") {
		t.Fatalf("clipboard=%q view=%s", copied, m.View())
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || m.quitting {
		t.Fatal("Ctrl+C quit with text selected")
	}
	m.Update(cmd())
	if len(copied) != 2 {
		t.Fatal("Ctrl+C did not copy selection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mouseSelection != nil {
		t.Fatal("Escape did not dismiss selection")
	}
}

func TestSelectionUsesWholeGraphemesAndPlainMultilineText(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lines      []string
		start, end screenPoint
		want       string
	}{
		{"reverse lines", []string{"\x1b[31mhello  \x1b[0m", "world"}, screenPoint{2, 1}, screenPoint{1, 0}, "ello\nwor"},
		{"CJK partial cell", []string{"A界B"}, screenPoint{2, 0}, screenPoint{3, 0}, "界B"},
		{"combining", []string{"Ae\u0301B"}, screenPoint{0, 0}, screenPoint{1, 0}, "Ae\u0301"},
		{"joined emoji", []string{"A👩‍💻B"}, screenPoint{0, 0}, screenPoint{1, 0}, "A👩‍💻"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mouseSelection{lines: tc.lines, start: tc.start, end: tc.end}
			if got := s.text(); got != tc.want {
				t.Fatalf("copied %q, want %q", got, tc.want)
			}
			for i, line := range strings.Split(s.view(80), "\n") {
				if ansi.Strip(line) != ansi.Strip(tc.lines[i]) || lipgloss.Width(line) != lipgloss.Width(tc.lines[i]) {
					t.Fatalf("highlight changed text/width: %q", line)
				}
			}
		})
	}
}

func TestSelectionRemainsStableWhileEventsContinue(t *testing.T) {
	m, _ := setup(t)
	m.add("Error", "original text", false)
	x, y := screenLocation(t, m.View(), "original")
	m.copyText = func(text string) error {
		if text != "original" {
			t.Errorf("copied changed output: %q", text)
		}
		return nil
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonLeft, x+7, y))
	frozen := m.View()
	_, listen := m.Update(received{event: conversation.MessageEvent{Message: message.Message{From: "root", To: message.User, Kind: message.Reply, Content: "new output"}}})
	if listen == nil || m.View() != frozen {
		t.Fatal("drag interrupted events or moved the selected text")
	}
	_, cmd := m.Update(mouseAt(tea.MouseActionRelease, tea.MouseButtonNone, x+7, y))
	m.Update(cmd())
	typeText(m, "draft")
	if m.mouseSelection != nil || m.input.Value() != "draft" || !strings.Contains(m.View(), "new output") {
		t.Fatal("typing did not resume live view")
	}
}

func TestSelectionClickWheelResizeAndNativeCopyMode(t *testing.T) {
	m, _ := setup(t)
	m.copyText = func(string) error { t.Fatal("unexpected clipboard write"); return nil }
	for range 20 {
		m.add("Error", "visible text", false)
	}
	x, y := screenLocation(t, m.View(), "visible text")
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	_, cmd := m.Update(mouseAt(tea.MouseActionRelease, tea.MouseButtonNone, x, y))
	if cmd != nil || m.mouseSelection != nil {
		t.Fatal("simple click copied text")
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonRight, x, y))
	if m.mouseSelection != nil {
		t.Fatal("right click started selection")
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonLeft, x+4, y))
	offset := m.viewport.YOffset
	m.Update(wheel(tea.MouseButtonWheelUp))
	if m.mouseSelection != nil || m.viewport.YOffset >= offset {
		t.Fatal("selection broke wheel scrolling")
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, 0, 0))
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	if m.mouseSelection != nil {
		t.Fatal("resize retained stale screen coordinates")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, 0, 0))
	if m.mouseSelection != nil || !m.selecting {
		t.Fatal("app selection interfered with native copy mode")
	}
}

func TestTranscriptMouseSelectionAndCopyFailure(t *testing.T) {
	m, _ := transcriptSetup(t)
	enter(m, "/transcript agent-7")
	m.copyText = func(string) error { return errors.New("clipboard unavailable") }
	x, y := screenLocation(t, m.View(), "Transcript")
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonLeft, x+9, y))
	_, cmd := m.Update(mouseAt(tea.MouseActionRelease, tea.MouseButtonLeft, x+9, y))
	m.Update(cmd())
	if m.transcript == nil || !strings.Contains(m.View(), "Copy failed: clipboard unavailable") {
		t.Fatal(m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.transcript == nil {
		t.Fatal("dismissing selection closed the transcript")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.transcript != nil {
		t.Fatal("transcript no longer closes")
	}
}

func TestLateClipboardResultDoesNotRestoreDismissedSelection(t *testing.T) {
	m, _ := setup(t)
	m.copyText = func(string) error { return nil }
	m.add("Strap", "clipboard text", false)
	x, y := screenLocation(t, m.View(), "clipboard text")
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	_, cmd := m.Update(mouseAt(tea.MouseActionRelease, tea.MouseButtonLeft, x+4, y))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(cmd())
	if m.mouseSelection != nil {
		t.Fatal("late result restored selection")
	}
}
