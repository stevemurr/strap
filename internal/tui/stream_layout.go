package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/message"
)

const rosterColumns = 30

func (m *model) streamChrome() int {
	if m.height >= 12 {
		return 3 // Stream title, inspector hints, and independent follow status.
	}
	return 0
}

func (m *model) sidebarWidth() int {
	if m.width >= 100 && m.streamChrome() != 0 {
		return rosterColumns + 3 // Padding and divider.
	}
	return 0
}

func (m *model) rosterHeight() int {
	if m.sidebarWidth() != 0 {
		return m.height - 2 // Everything below the header, including the composer.
	}
	return m.viewport.Height + m.streamChrome()
}

func inlineText(s string) string { return strings.Join(strings.Fields(safeText(s)), " ") }

func fitStreamCell(text string, width int) string {
	text = ansi.Truncate(text, max(1, width), "…")
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}

type rosterLine struct {
	text       string
	id         message.ActorID
	selectable bool
}

func (m *model) rosterLines(height, width int) []rosterLine {
	if m.selecting && m.streamUI.frozenRoster != nil {
		lines := make([]rosterLine, height)
		copy(lines, m.streamUI.frozenRoster)
		return lines
	}
	lines := []rosterLine{
		{text: stateStyle.Bold(true).Render(fmt.Sprintf("Agents  %d", len(m.streamUI.order)))},
		{text: dimStyle.Render(m.streamSummary())},
		{},
	}
	var rows []rosterLine
	selectedStart, selectedEnd := 0, 0
	// Compact automatically as the roster grows. The selected agent's full
	// task and activity remain available in its stream heading and transcript.
	rowHeight := min(6, max(2, (height-4)/max(1, len(m.streamUI.order))))
	for _, id := range m.streamChoices() {
		start := len(rows)
		selected := id == m.streamUI.selected
		marker := "  "
		if selected {
			marker = "› "
		}
		title := "All activity"
		if id != "" {
			title = inlineText(string(id)) + " · " + m.streamRole(id)
		}
		badge := ""
		if n := len(m.ensureStream(id).unread); n != 0 {
			badge = fmt.Sprintf(" +%d", n)
		}
		w, hasWork := m.streamWork(id)
		if m.streamNeedsAttention(id) {
			badge += " !"
		}
		label := ansi.Truncate(marker+title, max(1, width-ansi.StringWidth(badge)), "…") + stateStyle.Render(badge)
		if selected {
			label = routeStyle.Bold(m.streamUI.rosterFocused).Render(label)
		}
		block := []string{label}
		if id == "" {
			block = append(block, "")
		} else {
			state := m.streamState(id)
			if v := m.streamUI.views[id]; !v.since.IsZero() {
				state += " · " + elapsed(m.now().Sub(v.since))
			}
			style := dimStyle
			if m.working[id] {
				style = stateStyle
			}
			block = append(block, "  "+style.Render(state))
			if rowHeight > 2 {
				task := "No assigned work"
				if id == m.session.Root() {
					task = "Conversation & coordination"
				}
				if hasWork {
					task = inlineText(w.Task)
				}
				block = append(block, "  "+ansi.Truncate(task, width-2, "…"))
			}
			if rowHeight > 3 {
				detail := m.streamActivity(id)
				if hasWork {
					detail = workStatus(w)
					if w.Blocker != "" && !workFinished(w) {
						detail += ": " + inlineText(w.Blocker)
					} else if activity := m.streamActivity(id); activity != "" {
						detail += " · " + activity
					}
				}
				if v := m.streamUI.views[id]; v.err != "" {
					detail = "error: " + inlineText(v.err)
				}
				block = append(block, "  "+dimStyle.Render(ansi.Truncate(detail, width-2, "…")))
			}
			if rowHeight > 4 {
				context := "context unknown"
				if c := m.streamUI.views[id].context; c != nil {
					context = c.label()
					if !c.failed && !c.pending {
						context = tokenDigits(c.count) + " ctx · last count"
					}
				}
				block = append(block, "  "+dimStyle.Render(ansi.Truncate(context, width-2, "…")))
			}
			if rowHeight > 5 && id != m.streamUI.order[len(m.streamUI.order)-1] {
				block = append(block, "")
			}
		}
		for i, line := range block {
			if selected && line != "" {
				style := dimStyle
				if i == 0 {
					style = routeStyle.Bold(m.streamUI.rosterFocused)
				} else if i == 1 && m.working[id] {
					style = stateStyle
				} else if i == 2 {
					style = lipgloss.NewStyle()
				}
				line = style.Background(lipgloss.AdaptiveColor{Light: "254", Dark: "235"}).Render(fitStreamCell(ansi.Strip(line), width))
			}
			rows = append(rows, rosterLine{text: line, id: id, selectable: true})
		}
		if selected {
			selectedStart, selectedEnd = start, len(rows)
		}
	}
	available := max(1, height-len(lines))
	start := max(0, selectedEnd-available)
	start = min(start, selectedStart)
	if start > 0 {
		lines[2].text = dimStyle.Render("↑ more agents")
	}
	end := min(len(rows), start+available)
	lines = append(lines, rows[start:end]...)
	if end < len(rows) && len(lines) > 1 {
		// Keep the last selectable row; the header hints that the list continues.
		lines[0].text += dimStyle.Render("  ↓")
	}
	for len(lines) < height {
		lines = append(lines, rosterLine{})
	}
	return lines[:min(height, len(lines))]
}

func (m *model) streamTitle() string {
	id := m.streamUI.selected
	if id == "" {
		return titleStyle.Render("All activity")
	}
	title := inlineText(string(id)) + " · " + m.streamRole(id)
	if w, ok := m.streamWork(id); ok {
		title = inlineText(string(id)) + " · " + inlineText(w.Task)
	}
	state := m.streamState(id)
	width := max(1, m.viewport.Width-len(state)-3)
	return titleStyle.Render(ansi.Truncate(title, width, "…")) + "  " + stateStyle.Render(state)
}

func (m *model) streamFollowLabel() string {
	id := m.streamUI.selected
	if !m.viewport.AtBottom() {
		return fmt.Sprintf("History · %d new · Ctrl+End latest", len(m.ensureStream(id).unread))
	}
	label := "LIVE · following " + inlineText(string(id))
	if id == "" {
		label = "LIVE · all agents"
	}
	if activity := m.streamActivity(id); activity != "" {
		label += " · " + activity
	}
	return label
}

func (m *model) streamBody() string {
	height := m.viewport.Height + m.streamChrome()
	if m.streamUI.rosterFocused && m.sidebarWidth() == 0 {
		var rows []string
		for _, line := range m.rosterLines(height, max(1, m.width-2)) {
			rows = append(rows, line.text)
		}
		return strings.Join(rows, "\n")
	}
	var rows []string
	if m.streamChrome() != 0 {
		id := string(m.streamUI.selected)
		if id == "" {
			id = "root"
		}
		rows = append(rows, m.streamTitle(), dimStyle.Render("Stream · /transcript "+inlineText(id)+" · F6 agents"))
	}
	rows = append(rows, strings.Split(m.viewport.View(), "\n")...)
	if m.streamChrome() != 0 {
		rows = append(rows, dimStyle.Render(m.streamFollowLabel()))
	}
	return strings.Join(rows, "\n")
}

func (m *model) streamMouse(event tea.MouseMsg) bool {
	if m.mouseSelection != nil && m.mouseSelection.dragging {
		return false
	}
	width := m.sidebarWidth()
	if width == 0 && m.streamUI.rosterFocused {
		width = m.width
	}
	height := m.rosterHeight()
	if width == 0 || event.X >= width || event.Y < 2 || event.Y >= 2+height {
		return false
	}
	if event.Button == tea.MouseButtonWheelUp || event.Button == tea.MouseButtonWheelDown {
		m.mouseSelection = nil
		m.focusRoster(true)
		delta := 1
		if event.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		m.moveStream(delta)
		return true
	}
	if event.Button == tea.MouseButtonLeft && event.Action == tea.MouseActionPress {
		columns := rosterColumns
		if m.sidebarWidth() == 0 {
			columns = max(1, m.width-2)
		}
		rows := m.rosterLines(height, columns)
		if line := rows[event.Y-2]; line.selectable {
			m.mouseSelection = nil
			m.focusRoster(true)
			m.selectStream(line.id)
			return true
		}
	}
	return false
}
