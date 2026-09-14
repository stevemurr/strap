package tui

import (
	"strings"

	"github.com/atotto/clipboard"
	bindings "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func newInput() textarea.Model {
	input := textarea.New()
	input.Prompt = ""
	input.ShowLineNumbers = false
	input.FocusedStyle.Prompt = titleStyle
	input.FocusedStyle.Placeholder = dimStyle
	input.FocusedStyle.CursorLine = lipgloss.NewStyle()
	input.Placeholder = "Message Strap…"
	input.CharLimit = 0
	input.MaxWidth = 0
	input.SetHeight(1)
	input.FocusedStyle.Base = lipgloss.NewStyle().Background(composerBackground)
	input.BlurredStyle.Base = lipgloss.NewStyle().Background(composerBackground)
	input.KeyMap.InsertNewline = bindings.NewBinding(bindings.WithKeys("alt+enter", "ctrl+j"))
	input.Focus()
	return input
}

// Grow with the draft, reserving space for conversation output. Textarea owns
// wrapping and cursor scrolling when the draft exceeds the visible rows.
func (m *model) syncInputHeight() {
	limit := min(6, max(1, m.height-7-m.streamChrome()))
	if m.height < 8 {
		limit = min(6, m.height)
	}
	wrapped := ansi.Wrap(m.input.Value()+" ", m.input.Width(), "")
	rows := min(limit, strings.Count(wrapped, "\n")+1)
	if rows != m.input.Height() {
		line := m.input.Line()
		info := m.input.LineInfo()
		column := info.StartColumn + info.ColumnOffset
		m.input.SetHeight(rows)
		// Textarea retains its old scroll offset when its height changes.
		// Re-anchor at the beginning, then restore the exact editing position.
		m.input, _ = m.input.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
		for range line {
			m.input.CursorEnd()
			m.input.CursorDown()
		}
		m.input.SetCursor(column)
	}
	// SetWidth/SetHeight and direct clipboard insertion do not reposition the
	// textarea viewport themselves. Refresh its content before repositioning:
	// scrolling against the previous render would clamp to its old line count.
	_ = m.input.View()
	m.input, _ = m.input.Update(nil)
}

func normalizeInput(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

type inputPaste struct {
	text string
	err  error
}

func pasteInput() tea.Msg {
	text, err := clipboard.ReadAll()
	return inputPaste{text: text, err: err}
}
