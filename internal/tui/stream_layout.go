package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

const rosterColumns = 34

func (m *model) streamChrome() int {
	if m.height >= 12 {
		return 3 // Title, agent context, and space for history/error status.
	}
	return 0
}

func (m *model) rosterHeight() int {
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
	completed  bool
}

type rosterChoice struct {
	id        message.ActorID
	completed bool
}

var rosterGroups = []string{"Needs attention", "Working", "Idle", "Inactive", "Completed"}

func (m *model) rosterGroup(id message.ActorID) string {
	if m.streamNeedsAttention(id) {
		return "Needs attention"
	}
	if m.working[id] || m.streamState(id) == "running" || m.streamActivity(id) != "" {
		return "Working"
	}
	if w, ok := m.streamWork(id); ok && w.State == work.Accepted {
		return "Completed"
	}
	if state := m.streamState(id); state != "idle" {
		return "Inactive"
	}
	if w, ok := m.streamWork(id); ok && workFinished(w) {
		return "Inactive"
	}
	return "Idle"
}

func (m *model) groupedAgents(group string) []message.ActorID {
	var ids []message.ActorID
	for _, id := range m.streamUI.order {
		if id != m.session.Root() && m.rosterGroup(id) == group {
			ids = append(ids, id)
		}
	}
	return ids
}

func (m *model) rosterChoices() []rosterChoice {
	choices := []rosterChoice{{id: ""}, {id: m.session.Root()}}
	for _, group := range rosterGroups {
		ids := m.groupedAgents(group)
		if group == "Completed" && len(ids) > 0 {
			choices = append(choices, rosterChoice{completed: true})
			if !m.streamUI.completedExpanded {
				continue
			}
		}
		for _, id := range ids {
			choices = append(choices, rosterChoice{id: id})
		}
	}
	return choices
}

func (m *model) toggleCompleted() {
	if len(m.groupedAgents("Completed")) == 0 {
		return
	}
	m.streamUI.completedExpanded = !m.streamUI.completedExpanded
	if !m.streamUI.completedExpanded && m.rosterGroup(m.rosterCursor().id) == "Completed" {
		m.streamUI.completedFocused = true
	}
}

func (m *model) syncRosterFocus() {
	if len(m.groupedAgents("Completed")) == 0 && m.streamUI.completedFocused {
		m.streamUI.completedFocused = false
		m.streamUI.focusID = m.streamUI.selected
	}
	// A stream finishing work should not hide the chip currently being watched
	// or previewed. Explicitly focusing the disclosure still permits collapsing.
	if !m.streamUI.completedFocused && m.rosterGroup(m.rosterCursor().id) == "Completed" {
		m.streamUI.completedExpanded = true
	}
}

func (m *model) streamTask(id message.ActorID) string {
	if id == m.session.Root() {
		return "Conversation & coordination"
	}
	if w, ok := m.streamWork(id); ok && strings.TrimSpace(w.Task) != "" {
		return inlineText(w.Task)
	}
	return inlineText(string(id)) + " · " + m.streamRole(id)
}

func (m *model) rosterStatus(id message.ActorID) string {
	if m.ensureStream(id).err != "" {
		return "error"
	}
	if w, ok := m.streamWork(id); ok {
		if workFinished(w) && m.rosterGroup(id) != "Working" {
			return workStatus(w)
		}
		if m.streamNeedsAttention(id) {
			return workStatus(w)
		}
	}
	return m.streamState(id)
}

func (m *model) rosterLines(height, width int) []rosterLine {
	if height <= 0 {
		return nil
	}
	if m.selecting && m.streamUI.frozenRoster != nil {
		lines := make([]rosterLine, height)
		copy(lines, m.streamUI.frozenRoster)
		return lines
	}
	m.syncRosterFocus()
	lines := []rosterLine{{text: stateStyle.Bold(true).Render(fmt.Sprintf("Agents  %d", len(m.streamUI.order)))}, {text: dimStyle.Render(m.streamSummary())}, {}}
	var rows []rosterLine
	selectedStart, selectedEnd := 0, 0
	addAgent := func(id message.ActorID) {
		start := len(rows)
		selected := id == m.rosterCursor().id && !m.streamUI.completedFocused
		marker := "○ "
		switch m.rosterGroup(id) {
		case "Needs attention":
			marker = "! "
		case "Working":
			marker = "● "
		case "Completed":
			marker = "✓ "
		}
		title := m.streamTask(id)
		if id == "" {
			title = "All activity"
			marker = "  "
		}
		if selected {
			marker = "› "
		}
		badge := ""
		if n := len(m.ensureStream(id).unread); n > 0 {
			badge = fmt.Sprintf(" +%d", n)
		}
		label := fitStreamCell(marker+title, max(1, width-ansi.StringWidth(badge))) + badge
		block := []string{label}
		if id != "" {
			status := m.rosterStatus(id)
			left := "  " + inlineText(string(id))
			// Prefer the identifier over a long status in very narrow terminals.
			room := width - ansi.StringWidth(left) - 1
			if room > 0 {
				status = ansi.Truncate(status, room, "…")
				left = fitStreamCell(left, width-ansi.StringWidth(status)) + status
			}
			block = append(block, left)
		}
		for i, line := range block {
			style := dimStyle
			if i == 0 {
				style = lipgloss.NewStyle()
			}
			if m.streamNeedsAttention(id) && i == 1 {
				style = stateStyle
			}
			if selected {
				if i == 0 {
					style = routeStyle.Bold(m.streamUI.rosterFocused)
				}
				style = style.Background(lipgloss.AdaptiveColor{Light: "254", Dark: "235"})
			}
			rows = append(rows, rosterLine{text: style.Render(fitStreamCell(line, width)), id: id, selectable: true})
		}
		if selected {
			selectedStart, selectedEnd = start, len(rows)
		}
	}
	addAgent("")
	addAgent(m.session.Root())
	for _, group := range rosterGroups {
		ids := m.groupedAgents(group)
		if len(ids) == 0 {
			continue
		}
		rows = append(rows, rosterLine{})
		heading := fmt.Sprintf("%s  %d", group, len(ids))
		if group == "Completed" {
			marker := "▸ "
			if m.streamUI.completedExpanded {
				marker = "▾ "
			}
			style := dimStyle
			if m.streamUI.completedFocused {
				marker = "› " + marker
				style = routeStyle
			}
			if m.streamUI.completedFocused {
				selectedStart, selectedEnd = len(rows), len(rows)+1
			}
			rows = append(rows, rosterLine{text: style.Render(marker + heading), selectable: true, completed: true})
			if !m.streamUI.completedExpanded {
				continue
			}
		} else {
			style := dimStyle
			if group == "Needs attention" {
				style = stateStyle
			}
			rows = append(rows, rosterLine{text: style.Render(heading)})
		}
		for _, id := range ids {
			addAgent(id)
		}
	}
	available := max(1, height-len(lines))
	start := min(max(0, selectedEnd-available), selectedStart)
	if start > 0 {
		lines[2].text = dimStyle.Render("↑ more agents")
	}
	end := min(len(rows), start+available)
	lines = append(lines, rows[start:end]...)
	if end < len(rows) {
		lines[0].text += dimStyle.Render("  ↓")
	}
	for len(lines) < height {
		lines = append(lines, rosterLine{})
	}
	return lines[:min(height, len(lines))]
}

func (m *model) streamDetails() string {
	if m.selecting {
		return m.streamUI.frozenDetails
	}
	id := m.streamUI.selected
	if id == "" {
		return "All agents"
	}
	parts := []string{inlineText(string(id)), m.streamRole(id)}
	if parent := m.ensureStream(id).parent; parent != "" {
		parts = append(parts, "parent "+inlineText(string(parent)))
	}
	return strings.Join(parts, " · ")
}

func (m *model) streamTitle() string {
	if m.selecting {
		return m.streamUI.frozenTitle
	}
	id := m.streamUI.selected
	if id == "" {
		state := "idle"
		if m.busy() {
			state = "working"
		}
		return titleStyle.Render("All activity") + "  " + stateStyle.Render(state)
	}
	title := m.streamRole(id)
	if _, ok := m.streamWork(id); ok || id == m.session.Root() {
		title = m.streamTask(id)
	}
	state := m.rosterStatus(id)
	if id == m.session.Root() && state == "idle" && len(m.working) > 0 {
		state = fmt.Sprintf("%d agent(s) working", len(m.working))
	}
	width := max(1, m.viewport.Width-len(state)-3)
	return titleStyle.Render(ansi.Truncate(title, width, "…")) + "  " + stateStyle.Render(state)
}

func (m *model) streamFollowLabel() string {
	if m.selecting {
		return m.streamUI.frozenFollow
	}
	id := m.streamUI.selected
	if !m.viewport.AtBottom() {
		return fmt.Sprintf("History · %d new · Ctrl+End latest", len(m.ensureStream(id).unread))
	}
	label := "LIVE · following " + inlineText(string(id))
	if id == "" {
		label = "LIVE · all agents"
	}
	if v := m.streamUI.views[id]; v != nil && v.err != "" {
		return label + " · error: " + inlineText(v.err)
	}
	if w, ok := m.streamWork(id); ok && !workFinished(w) && w.Blocker != "" {
		return label + " · blocked: " + inlineText(w.Blocker)
	}
	if m.closed || m.rootStopped {
		return m.status()
	}
	return ""
}

func (m *model) streamBody() string {
	height := m.viewport.Height + m.streamChrome()
	if m.streamUI.rosterFocused && m.stackBarHeight() == 0 {
		var rows []string
		for _, line := range m.rosterLines(height, max(1, m.width-2)) {
			rows = append(rows, line.text)
		}
		return strings.Join(rows, "\n")
	}
	var rows []string
	if m.streamChrome() != 0 {
		rows = append(rows, m.streamTitle(), dimStyle.Render(m.streamDetails()))
	}
	rows = append(rows, strings.Split(m.viewport.View(), "\n")...)
	if m.streamChrome() != 0 {
		rows = append(rows, dimStyle.Render(m.streamFollowLabel()))
	}
	return strings.Join(rows, "\n")
}

func (m *model) streamMouse(event tea.MouseMsg) bool {
	if m.stackBarHeight() != 0 {
		return m.stackMouse(event)
	}
	if m.mouseSelection != nil && m.mouseSelection.dragging {
		return false
	}
	width := 0
	if m.streamUI.rosterFocused {
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
		columns := max(1, m.width-2)
		rows := m.rosterLines(height, columns)
		if line := rows[event.Y-2]; line.selectable {
			m.mouseSelection = nil
			m.focusRoster(true)
			if line.completed {
				m.streamUI.completedFocused = true
				m.toggleCompleted()
			} else {
				m.selectStream(line.id)
				m.focusRoster(false)
			}
			return true
		}
	}
	return false
}
