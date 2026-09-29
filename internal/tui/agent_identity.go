package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/message"
)

// An agent is told apart by its color: the dot that leads each of its rows is
// painted in it, and hovering the dot names the agent.
func agentDot(id message.ActorID) string { return stackIdentity(id).Render("●") }

type agentBadgeTarget struct {
	id          message.ActorID
	row, column int
}
type badgeState struct {
	targets []agentBadgeTarget
	peek    *stackPreview
}

func (m *model) badgeMouse(event tea.MouseMsg, originX, originY, width, height int) bool {
	if m.selecting || (m.mouseSelection != nil && m.mouseSelection.dragging) {
		return false
	}
	if tea.MouseEvent(event).IsWheel() {
		m.badges.peek = nil
		return false
	}
	if p := m.badges.peek; p != nil && event.X >= p.x && event.X < p.x+lipgloss.Width(p.text) && event.Y >= p.y && event.Y < p.y+lipgloss.Height(p.text) {
		return true
	}
	// Hover only: a click on a tool row's dot opens its output.
	if event.Y >= originY && event.Y < originY+m.viewport.Height && event.Action == tea.MouseActionMotion {
		for _, t := range m.badges.targets {
			x, y := originX+t.column, originY+t.row-m.viewport.YOffset
			if event.X != x || event.Y != y {
				continue
			}
			m.streamUI.hovering = false
			w := min(58, width-2)
			if w < 12 || height < 7 {
				return true
			}
			inner := w - 4
			details := inlineText(string(t.id)) + " · " + m.streamRole(t.id)
			lines := []string{agentDot(t.id) + " " + m.streamTask(t.id), details, m.rosterStatus(t.id), m.stackLatest(t.id)}
			for i, line := range lines {
				lines[i] = ansi.Truncate(line, inner, "…")
			}
			panelStyle := lipgloss.NewStyle().Foreground(surfaceTextColor).Background(composerBackground).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.AdaptiveColor{Light: "250", Dark: "240"}).Padding(0, 1)
			panel := renderSurface(panelStyle, strings.Join(lines, "\n"))
			m.badges.peek = &stackPreview{x: max(0, min(x, width-lipgloss.Width(panel))), y: max(0, min(y+1, height-lipgloss.Height(panel))), text: panel}
			return true
		}
	}
	m.badges.peek = nil
	return false
}

func (m *model) overlayBadgePeek(view string, width, height int) string {
	if p := m.badges.peek; p != nil {
		s := newChipSurface(width, height)
		s.paint(0, 0, view, -1)
		s.paint(p.x, p.y, p.text, -1)
		return s.String()
	}
	return view
}

func (m *model) renderMessage(e *entry, firstRow int) string {
	if e.reportDetail != nil || e.label == "Work" || e.label == "Progress" {
		return m.renderProgress(e, firstRow)
	}
	width := max(1, m.viewport.Width-1)
	if e.label == "You" {
		surface := lipgloss.NewStyle().Foreground(surfaceTextColor).Background(lipgloss.AdaptiveColor{Light: "#F4F4F4", Dark: "#262626"})
		padding := surface.Render(strings.Repeat(" ", width))
		lines := []string{padding}
		for i, line := range strings.Split(ansi.Hardwrap(e.body, max(1, width-3), true), "\n") {
			prefix := "  "
			if i == 0 {
				prefix = accentStyle.Bold(true).Render("›") + " "
			}
			lines = append(lines, renderSurface(surface, fitStreamCell(prefix+line, width)))
		}
		lines = append(lines, padding)
		return strings.Join(lines, "\n")
	}
	if e.label == "Strap" || e.label == "Message" || e.output != nil {
		actor := m.session.Manager()
		if len(e.actors) > 0 {
			actor = e.actors[0]
		}
		prefix := agentDot(actor) + " "
		if len(e.actors) > 0 && e.actors[0] != m.session.Manager() {
			m.badges.targets = append(m.badges.targets, agentBadgeTarget{id: e.actors[0], row: firstRow, column: 0})
		}
		indent := ansi.StringWidth(prefix)
		body := m.renderBodyWidth(e, max(1, width-indent))
		var lines []string
		for _, line := range strings.Split(body, "\n") {
			lines = append(lines, strings.Split(ansi.Hardwrap(strings.TrimRight(line, " "), max(1, width-indent), true), "\n")...)
		}
		for i, line := range lines {
			if i == 0 {
				lines[i] = prefix + line
			} else {
				lines[i] = strings.Repeat(" ", indent) + line
			}
			lines[i] = ansi.Truncate(lines[i], width, "")
		}
		if e.outputFailed {
			lines = append(lines, errorStyle.Render(ansi.Truncate("! "+e.meta, width, "…")))
		}
		return strings.Join(lines, "\n")
	}
	style := dimStyle
	if e.label == "Error" {
		style = errorStyle
	}
	heading := style.Render(e.label)
	if e.meta != "" {
		heading += " · " + dimStyle.Render(e.meta)
	}
	return ansi.Truncate(heading, width, "…") + "\n" + m.renderBody(e)
}
