package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var composerBackground = lipgloss.AdaptiveColor{Light: "254", Dark: "235"}

func (m *model) composerInset() int {
	if m.height < 8 || m.viewport.Width < 10 {
		return 0
	}
	return 5 // One cell left padding, a gap, and the send action.
}

func (m *model) transcriptTop() int {
	if m.streamChrome() != 0 {
		return 4
	}
	return 2
}

func (m *model) composerTop() int {
	return 2 + m.viewport.Height + m.streamChrome() + m.completionHeight()
}

func (m *model) composerView() []string {
	width := m.viewport.Width
	surface := lipgloss.NewStyle().Background(composerBackground)
	blank := surface.Render(strings.Repeat(" ", width))
	lines := []string{blank}
	for _, row := range strings.Split(m.input.View(), "\n") {
		if m.composerInset() > 0 {
			row = " " + row
		}
		row = surface.Render(fitStreamCell(row, width))
		// Textarea's nested text/cursor styles reset their background. Restore
		// the field surface after resets so its trailing spaces stay shaded.
		if background := sgrPattern.FindString(surface.Render(" ")); background != "" {
			row = strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+background) + "\x1b[0m"
		}
		lines = append(lines, row)
	}
	last := blank
	if width >= 10 {
		style := dimStyle.Background(composerBackground)
		if strings.TrimSpace(m.input.Value()) != "" && !m.selecting {
			style = userStyle.Foreground(lipgloss.AdaptiveColor{Light: "255", Dark: "235"}).Background(lipgloss.AdaptiveColor{Light: "235", Dark: "252"})
		}
		last = surface.Render(strings.Repeat(" ", width-3)) + style.Render(" ↑ ")
	}
	lines = append(lines, last)
	return lines
}

func (m *model) composerMouse(event tea.MouseMsg) (bool, tea.Cmd) {
	if m.height < 8 || event.Action != tea.MouseActionPress || event.Button != tea.MouseButtonLeft {
		return false, nil
	}
	left, top := 1+m.sidebarWidth(), m.composerTop()
	if event.X < left || event.X >= left+m.viewport.Width || event.Y < top || event.Y > top+m.input.Height()+1 {
		return false, nil
	}
	m.folds.focused = false
	m.focusRoster(false)
	if m.viewport.Width >= 10 && event.Y == top+m.input.Height()+1 && event.X >= left+m.viewport.Width-3 {
		if strings.TrimSpace(m.input.Value()) == "" {
			return true, nil
		}
		// Use exactly the keyboard send/command path, including completion.
		if m.completionKey("enter") {
			return true, nil
		}
		_, cmd := m.submit()
		return true, cmd
	}
	return false, nil
}

func (m *model) composerHint() string {
	if m.selecting {
		return m.footer()
	}
	if m.streamUI.rosterFocused {
		return "↑/↓ select · Enter compose · F6 return"
	}
	if m.folds.focused {
		return "↑/↓ fold · Enter expand · Esc compose"
	}
	if m.completionHeight() > 0 {
		return "↑/↓ select · Tab complete · Enter confirm · Esc dismiss"
	}
	if m.input.Focused() && m.input.Value() != "" {
		return "/ commands · Enter send · Alt+Enter newline"
	}
	return ""
}

func (m *model) renderComposer() []string {
	lines := m.composerView()
	return append(lines, dimStyle.Render(ansi.Truncate(m.composerHint(), m.viewport.Width, "…")))
}
