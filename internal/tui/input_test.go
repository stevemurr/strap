package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

func TestMultilineInputNewlinesAndSend(t *testing.T) {
	for _, newline := range []tea.KeyMsg{{Type: tea.KeyEnter, Alt: true}, {Type: tea.KeyCtrlJ}} {
		m, s := setup(t)
		typeText(m, "  first line")
		m.Update(newline)
		typeText(m, "    second line")
		m.Update(newline)
		want := "  first line\n    second line\n"
		if len(s.sent) != 0 || m.input.Value() != want || m.input.Height() != 3 {
			t.Fatalf("newline %s: draft=%q sent=%v height=%d", newline, m.input.Value(), s.sent, m.input.Height())
		}
		if view := ansi.Strip(m.View()); !strings.Contains(view, "first line") || !strings.Contains(view, "second line") {
			t.Fatal("multiline draft not visible", view)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if len(s.sent) != 1 || s.sent[0] != want || m.history[0] != want {
			t.Fatalf("send changed whitespace or split message: %q", s.sent)
		}
		if m.input.Value() != "" || m.input.Height() != 1 {
			t.Fatal("send did not reset composer")
		}
	}
}

func TestMultilinePasteDoesNotExecuteCommands(t *testing.T) {
	for _, clipboard := range []bool{false, true} {
		m, s := setup(t)
		raw := "/quit\r\n    explain this\r\n"
		if clipboard {
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
			if cmd == nil {
				t.Fatal("clipboard read not scheduled")
			}
			m.Update(inputPaste{text: raw})
		} else {
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(raw), Paste: true})
		}
		want := "/quit\n    explain this\n"
		if m.quitting || len(s.sent) != 0 || m.input.Value() != want || len(m.completionMatches()) != 0 {
			t.Fatalf("paste executed command, collapsed lines, or opened completion: %q", m.input.Value())
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.quitting || len(s.sent) != 1 || s.sent[0] != want {
			t.Fatal("multiline prompt treated as slash command", s.sent)
		}
	}
}

func TestMultilineEditingAndHistoryPreserveDraft(t *testing.T) {
	m, _ := setup(t)
	enter(m, "old\nmessage")
	typeText(m, "first")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeText(m, "second")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Line() != 0 || m.input.Value() != "first\nsecond" {
		t.Fatal("up recalled history instead of moving cursor")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	typeText(m, "edited ")
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.input.Line() != 1 {
		t.Fatal("down did not navigate draft")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input.Value() != "edited firstsecond" {
		t.Fatal("backspace did not join lines", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	want := m.input.Value()
	m.Update(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if m.input.Value() != "old\nmessage" {
		t.Fatal("explicit history did not recall multiline message")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	if m.input.Value() != want {
		t.Fatal("history lost multiline draft", m.input.Value())
	}
}

func TestWrappedInputNavigationAndResizeKeepCursorVisible(t *testing.T) {
	m, _ := setup(t)
	enter(m, "previous message")
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 20})
	typeText(m, "alpha beta gamma delta epsilon zeta eta theta LAST")
	want := m.input.Value()
	if m.input.Height() < 2 {
		t.Fatal("long line did not expand")
	}
	info := m.input.LineInfo()
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != want || m.input.LineInfo().RowOffset >= info.RowOffset {
		t.Fatal("wrapped navigation recalled history")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	for _, size := range []tea.WindowSizeMsg{{Width: 14, Height: 10}, {Width: 80, Height: 24}, {Width: 10, Height: 5}, {Width: 1, Height: 1}} {
		m.Update(size)
		view := m.View()
		assertFits(t, view, size.Width, size.Height)
		if size.Width > 1 && !strings.Contains(ansi.Strip(view), "LAST") {
			t.Fatalf("cursor tail invisible after resize: %dx%d\n%s", size.Width, size.Height, view)
		}
		if m.input.Value() != want {
			t.Fatal("resize modified draft")
		}
	}
}

func TestLargeDraftScrollFreezeAndIncomingMessages(t *testing.T) {
	m, s := setup(t)
	for i := range 30 {
		m.add("Strap", fmt.Sprintf("answer %d", i), true)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	offset := m.viewport.YOffset
	want := strings.Repeat("draft line\n", 20) + "END"
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(want), Paste: true})
	if m.input.Height() <= 6 || m.viewport.YOffset != offset || !strings.Contains(m.input.View(), "END") {
		t.Fatal("draft growth lost scroll or cursor")
	}
	m.Update(received{event: conversation.MessageEvent{Message: message.Message{From: "root", To: message.User, Kind: message.Reply, Content: "incoming"}}})
	if m.input.Value() != want || m.viewport.YOffset != offset {
		t.Fatal("incoming output changed draft/scroll")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.Update(inputPaste{text: "paste while frozen"})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.View() != frozen || m.input.Value() != want || len(s.sent) != 0 {
		t.Fatal("frozen draft changed")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(s.sent) != 1 || s.sent[0] != want {
		t.Fatal("large draft truncated", s.sent)
	}
}

func TestDraftGrowsToAvailableHeightAndShrinks(t *testing.T) {
	m, s := setup(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 180})
	for i := range 120 {
		typeText(m, fmt.Sprintf("line %03d", i))
		m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	typeText(m, "END")
	want := m.input.Value()
	if m.input.LineCount() != 121 || m.input.Height() != 121 {
		t.Fatalf("draft stopped growing: %d lines, %d visible", m.input.LineCount(), m.input.Height())
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 40}, {Width: 20, Height: 12}, {Width: 10, Height: 7}, {Width: 100, Height: 180}} {
		m.Update(size)
		if m.input.Value() != want || !strings.Contains(ansi.Strip(m.input.View()), "END") {
			t.Fatalf("resize lost draft or cursor at %dx%d", size.Width, size.Height)
		}
		if rows := strings.Count(m.View(), "\n") + 1; rows > size.Height {
			t.Fatalf("view uses %d rows at height %d", rows, size.Height)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(s.sent) != 1 || s.sent[0] != want || m.input.Height() != 1 {
		t.Fatal("send lost content or did not shrink the composer")
	}
}

func TestMultilineSendAndPasteFailuresKeepDraft(t *testing.T) {
	m, s := setup(t)
	typeText(m, "first\nsecond")
	m.Update(inputPaste{err: errors.New("clipboard unavailable")})
	if m.input.Value() != "first\nsecond" || !strings.Contains(m.entries[len(m.entries)-1].body, "Paste failed") {
		t.Fatal("paste failure lost draft or diagnostic")
	}
	s.err = errors.New("offline")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.input.Value() != "first\nsecond" {
		t.Fatal("send failure lost draft")
	}
	s.err = nil
	m.input.SetValue(" \n  ")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(s.sent) != 0 {
		t.Fatal("sent empty multiline input")
	}
}

func TestUnicodeMultilineInputAndIndentationSurviveResize(t *testing.T) {
	m, s := setup(t)
	typeText(m, "汉字🙂 café")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	typeText(m, "日本語")
	want := "汉字🙂 café\n    日本語"
	for _, size := range []tea.WindowSizeMsg{{Width: 5, Height: 9}, {Width: 1, Height: 1}, {Width: 80, Height: 24}} {
		m.Update(size)
		if m.input.Value() != want {
			t.Fatal("resize changed Unicode/indentation", m.input.Value())
		}
		info := m.input.LineInfo()
		if m.input.Line() != 1 || info.StartColumn+info.ColumnOffset != 7 {
			t.Fatalf("resize moved Unicode cursor: line %d column %d", m.input.Line(), info.StartColumn+info.ColumnOffset)
		}
		assertFits(t, m.View(), size.Width, size.Height)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(s.sent) != 1 || s.sent[0] != want {
		t.Fatal("send changed Unicode/indentation", s.sent)
	}
}
