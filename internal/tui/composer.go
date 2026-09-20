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

func (m *model) transcriptTop() int { return m.stackBarHeight() }

func (m *model) composerTop() int {
	return 1 + m.stackBarHeight() + m.viewport.Height + m.streamChrome() + m.completionHeight() + m.planHeight()
}

func (m *model) composerActivity() string {
	if !m.busy() && !m.interrupting {
		return ""
	}
	label := "Working"
	hint := " · Esc to stop"
	if m.interrupting {
		label, hint = "Stopping", ""
	}
	// Reuse the existing spinner clock; only this row changes on a tick.
	style := stackIdentity(m.session.Root())
	if m.interrupting {
		style = stateStyle
	}
	icon := style.Render(ansi.Strip(m.spinner.View()))
	return ansi.Truncate(" "+icon+" "+style.Bold(true).Render(label)+dimStyle.Render(hint), m.viewport.Width, "…")
}

func (m *model) composerView() []string {
	width := m.viewport.Width
	surface := lipgloss.NewStyle().Foreground(surfaceTextColor).Background(composerBackground)
	blank := surface.Render(strings.Repeat(" ", width))
	lines := []string{blank}
	for _, row := range strings.Split(m.input.View(), "\n") {
		if m.composerInset() > 0 {
			row = " " + row
		}
		row = renderSurface(surface, fitStreamCell(row, width))
		lines = append(lines, row)
	}
	last := blank
	if width >= 10 {
		style := dimStyle.Background(composerBackground)
		if strings.TrimSpace(m.input.Value()) != "" && !m.selecting {
			style = selectedTextStyle.Bold(true)
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
	left, top := 1, m.composerTop()
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
	if m.plans.focused {
		return "↑/↓ steps · Enter details · [/] plans · Esc compose"
	}
	if m.streamUI.rosterFocused {
		return "←/→ preview · Enter open · Esc compose · c completed"
	}
	if m.folds.focused {
		return "↑/↓ fold · Enter expand · Esc compose"
	}
	if m.completionHeight() > 0 {
		return "↑/↓ select · Tab complete · Enter confirm · Esc dismiss"
	}
	if m.input.Focused() && m.input.Value() != "" {
		return "/ commands · Enter send · Shift+Enter newline"
	}
	if m.currentPlan() != nil {
		return "Ctrl+P plan · F8 steps"
	}
	return ""
}

func (m *model) renderComposer() []string {
	// Activity has its own reserved row above the field's shaded top padding.
	// Keep it even when idle so starting work never moves the draft.
	lines := []string{fitStreamCell(m.composerActivity(), m.viewport.Width)}
	lines = append(lines, m.composerView()...)
	return append(lines, dimStyle.Render(ansi.Truncate(m.composerHint(), m.viewport.Width, "…")))
}
