package tui

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/message"
)

const stackChipWidth, stackChipStride = 16, 10

// The strip wraps when there is room to keep the whole team visible. Small
// terminals retain the F6 list rather than losing their remaining text rows.
func (m *model) stackBarHeight() int {
	if m.width >= 40 && m.height >= 18 {
		if m.selecting && len(m.streamUI.frozenStacks) > 0 {
			return min(len(m.streamUI.frozenStacks), m.stackHeightBudget())
		}
		return m.stackLayout().height + 1
	}
	return 0
}

type stackTarget struct {
	choice              rosterChoice
	x, y, width, height int
}

type stackLayout struct {
	targets               []stackTarget
	headings              []stackHeading
	width, height, offset int
}

type stackHeading struct {
	x, y int
	text string
}

type stackPreview struct {
	x, y int
	text string
}

func (m *model) rosterCursor() rosterChoice {
	if m.streamUI.rosterFocused {
		if m.streamUI.completedFocused {
			return rosterChoice{completed: true}
		}
		return rosterChoice{id: m.streamUI.focusID}
	}
	return rosterChoice{id: m.streamUI.selected}
}

// Reserve room for the transcript, stream heading, and composer. Each band
// has one heading row and three chip rows, followed by one shared separator.
func (m *model) stackHeightBudget() int {
	return max(5, ((m.height-14)/4)*4+1)
}

func (m *model) stackLayout() stackLayout {
	m.syncRosterFocus()
	available := max(1, m.width-4)
	l := m.layoutStackBands(available)
	if l.height+1 <= m.stackHeightBudget() {
		return l
	}
	// On short terminals, retain horizontal navigation instead of consuming
	// the conversation. Every agent remains reachable by keyboard or wheel.
	l = m.layoutStackBands(0)
	l.offset = min(m.streamUI.stackOffset, max(0, l.width-available))
	cursor := m.rosterCursor()
	for _, target := range l.targets {
		if target.choice != cursor {
			continue
		}
		if target.x < l.offset {
			l.offset = target.x
		}
		if target.x+target.width > l.offset+available {
			l.offset = target.x + target.width - available
		}
		break
	}
	l.offset = max(0, l.offset)
	return l
}

func (m *model) layoutStackBands(available int) stackLayout {
	l := stackLayout{height: 4, width: stackChipWidth, targets: []stackTarget{
		{choice: rosterChoice{id: ""}, x: 0, y: 0, width: stackChipWidth, height: 1},
		{choice: rosterChoice{id: m.session.Root()}, x: 0, y: 1, width: stackChipWidth, height: 3},
	}}
	x, y := stackChipWidth+2, 0
	for _, group := range rosterGroups {
		ids := m.groupedAgents(group)
		if len(ids) == 0 {
			continue
		}
		heading := fmt.Sprintf("%s  %d", group, len(ids))
		if group == "Completed" {
			marker := "▸ "
			if m.streamUI.completedExpanded {
				marker = "▾ "
			}
			heading = marker + heading
		}
		if group == "Completed" && !m.streamUI.completedExpanded {
			ids = nil
		}
		for first := true; first || len(ids) > 0; first = false {
			width := ansi.StringWidth(heading)
			if len(ids) > 0 {
				width = max(width, stackChipWidth+(len(ids)-1)*stackChipStride)
			}
			if available > 0 && x > 0 && x+width > available {
				x, y = 0, y+4
			}
			count := len(ids)
			if available > 0 && count > 0 {
				count = min(count, max(1, 1+(available-x-stackChipWidth)/stackChipStride))
				width = max(ansi.StringWidth(heading), stackChipWidth+(count-1)*stackChipStride)
			}
			if group == "Completed" && first {
				l.targets = append(l.targets, stackTarget{choice: rosterChoice{completed: true}, x: x, y: y, width: ansi.StringWidth(heading), height: 1})
			} else {
				l.headings = append(l.headings, stackHeading{x: x, y: y, text: heading})
			}
			for i, id := range ids[:count] {
				l.targets = append(l.targets, stackTarget{choice: rosterChoice{id: id}, x: x + i*stackChipStride, y: y + 1, width: stackChipWidth, height: 3})
			}
			l.width = max(l.width, x+width)
			l.height = y + 4
			x += width + 2
			ids = ids[count:]
		}
	}
	return l
}

func stackIdentity(id message.ActorID) lipgloss.Style {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	colors := []lipgloss.AdaptiveColor{{Light: "25", Dark: "111"}, {Light: "30", Dark: "116"}, {Light: "90", Dark: "183"}, {Light: "28", Dark: "114"}, {Light: "130", Dark: "215"}, {Light: "91", Dark: "177"}}
	return lipgloss.NewStyle().Foreground(colors[int(h.Sum32())%len(colors)])
}

func (m *model) stackTargetView(t stackTarget, highlight bool) string {
	style := dimStyle
	if t.choice.completed {
		marker := "▸ "
		if m.streamUI.completedExpanded {
			marker = "▾ "
		}
		if highlight {
			style = routeStyle.Bold(true)
		}
		return style.Render(fmt.Sprintf("%sCompleted  %d", marker, len(m.groupedAgents("Completed"))))
	}
	id := t.choice.id
	if t.height == 1 {
		name := "All activity"
		if id != "" {
			name = "root"
		}
		marker := "  "
		if id == m.streamUI.selected {
			marker = "› "
		}
		if highlight || id == m.streamUI.selected {
			style = routeStyle.Background(composerBackground)
		}
		return style.Render(fitStreamCell(marker+name, t.width))
	}
	style = stackIdentity(id)
	if highlight || id == m.streamUI.selected {
		style = style.Background(composerBackground).Bold(true)
	}
	mark := "○"
	switch m.rosterGroup(id) {
	case "Needs attention":
		mark = "!"
	case "Working":
		mark = "●"
	case "Completed":
		mark = "✓"
	}
	name := m.streamTask(id)
	if id == m.session.Root() {
		name = "root"
	}
	label := mark + " " + name
	return style.Border(lipgloss.RoundedBorder()).Render(fitStreamCell(label, stackChipWidth-2))
}

// The hit surface always uses base paint order. A visually lifted chip must
// never steal its neighbour's hit target or flicker as the pointer moves.
func (m *model) stackSurface(l stackLayout, lift bool) *chipSurface {
	s := newChipSurface(max(1, m.width-4), l.height)
	for _, h := range l.headings {
		style := dimStyle
		if strings.HasPrefix(h.text, "Needs attention") || h.x == 0 {
			style = stateStyle
		}
		s.paint(h.x-l.offset, h.y, style.Render(h.text), -1)
	}
	cursor := m.rosterCursor()
	for i, t := range l.targets {
		s.paint(t.x-l.offset, t.y, m.stackTargetView(t, m.streamUI.rosterFocused && cursor == t.choice), i)
	}
	if lift {
		choice, ok := m.stackPreviewChoice()
		if ok {
			for i, t := range l.targets {
				if t.choice == choice {
					s.paint(t.x-l.offset, t.y, m.stackTargetView(t, true), i)
					break
				}
			}
		}
	}
	return s
}

func (m *model) stackBar() []string {
	if m.stackBarHeight() == 0 {
		return nil
	}
	if m.selecting {
		rows := make([]string, m.stackBarHeight())
		copy(rows, m.streamUI.frozenStacks)
		return rows
	}
	l := m.stackLayout()
	m.streamUI.stackOffset = l.offset
	s := m.stackSurface(l, true)
	rows := make([]string, l.height+1)
	for y, row := range s.rows {
		left, right := " ", " "
		if y == 2 {
			if l.offset > 0 {
				left = routeStyle.Render("‹")
			}
			if l.offset+s.width < l.width {
				right = routeStyle.Render("›")
			}
		}
		rows[y] = left + row + right
	}
	rows[l.height] = dimStyle.Render(strings.Repeat("─", max(1, m.width-2)))
	return rows
}

func (m *model) stackPreviewChoice() (rosterChoice, bool) {
	if m.streamUI.hovering {
		return m.streamUI.hover, true
	}
	if m.streamUI.rosterFocused {
		return m.rosterCursor(), true
	}
	return rosterChoice{}, false
}

func (m *model) stackLatest(id message.ActorID) string {
	if id == "" {
		return m.streamSummary()
	}
	if v := m.streamUI.views[id]; v != nil && v.err != "" {
		return inlineText(v.err)
	}
	if w, ok := m.streamWork(id); ok && !workFinished(w) && w.Blocker != "" {
		return inlineText(w.Blocker)
	}
	if activity := m.streamActivity(id); activity != "" {
		return inlineText(activity)
	}
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := &m.entries[i]
		if e.label != "Tool" && e.body != "" && slices.Contains(e.actors, id) {
			return inlineText(e.body)
		}
	}
	return "No recent update."
}

func (m *model) stackPeek() *stackPreview {
	if m.stackBarHeight() == 0 {
		return nil
	}
	if m.selecting {
		return m.streamUI.frozenPeek
	}
	choice, ok := m.stackPreviewChoice()
	if !ok || choice.completed {
		return nil
	}
	id := choice.id
	title, details, status := m.streamTask(id), inlineText(string(id))+" · "+m.streamRole(id), m.rosterStatus(id)
	if id == "" {
		title, details, status = "All activity", "All agents", m.streamSummary()
	}
	if parent := m.ensureStream(id).parent; parent != "" {
		details += " · parent " + inlineText(string(parent))
	}
	if execution := m.streamState(id); id != "" && execution != status {
		status += " · " + execution
	}
	width := min(62, m.width-4)
	inner := width - 4
	available := m.composerTop() - (2 + m.stackBarHeight())
	// Reserve two border rows and the footer. Never obscure the composer.
	if available < 5 {
		return nil
	}
	lines := []string{titleStyle.Render(ansi.Truncate(title, inner, "…")), dimStyle.Render(ansi.Truncate(details, inner, "…")), stateStyle.Render(ansi.Truncate(status, inner, "…"))}
	if available >= 9 {
		lines = append(lines, "")
	}
	lines = append(lines, strings.Split(ansi.Hardwrap(ansi.Truncate(m.stackLatest(id), inner*4, "…"), inner, true), "\n")...)
	if len(lines) > available-3 {
		lines = lines[:available-3]
	}
	hint := "click to open"
	if m.streamUI.rosterFocused {
		hint = "Enter / click to open"
	}
	lines = append(lines, dimStyle.Render(ansi.Truncate(fmt.Sprintf("%d unread · %s", len(m.ensureStream(id).unread), hint), inner, "…")))
	panel := lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "255", Dark: "234"}).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.AdaptiveColor{Light: "30", Dark: "116"}).Padding(0, 1).Render(strings.Join(lines, "\n"))
	x := 2
	l := m.stackLayout()
	for _, t := range l.targets {
		if t.choice == choice {
			x = t.x - l.offset + 2
			break
		}
	}
	x = max(1, min(x, m.width-lipgloss.Width(panel)-1))
	return &stackPreview{x: x, y: 2 + m.stackBarHeight(), text: panel}
}

func (m *model) stackMouse(event tea.MouseMsg) bool {
	if m.mouseSelection != nil && m.mouseSelection.dragging {
		return false
	}
	if p := m.stackPeek(); p != nil && event.X >= p.x && event.X < p.x+lipgloss.Width(p.text) && event.Y >= p.y && event.Y < p.y+lipgloss.Height(p.text) {
		if tea.MouseEvent(event).IsWheel() {
			m.focusRoster(false)
			return false
		}
		if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
			choice, ok := m.stackPreviewChoice()
			if ok {
				m.selectStream(choice.id)
				m.focusRoster(false)
			}
		}
		return true
	}
	if event.Y < 2 || event.Y >= 2+m.stackBarHeight() || event.X < 1 || event.X >= m.width-1 {
		m.streamUI.hovering = false
		if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
			m.focusRoster(false)
		}
		return false
	}
	if tea.MouseEvent(event).IsWheel() {
		if !m.streamUI.rosterFocused {
			m.focusRoster(true)
		}
		delta := 1
		if event.Button == tea.MouseButtonWheelUp || event.Button == tea.MouseButtonWheelLeft {
			delta = -1
		}
		m.moveStream(delta)
		return true
	}
	l := m.stackLayout()
	base := m.stackSurface(l, false)
	index := base.hit(event.X-2, event.Y-2)
	if event.Action == tea.MouseActionMotion && event.Button == tea.MouseButtonNone {
		// Keep the preview reachable across the separator row between its
		// trigger and panel; the gap itself is not a new interactive target.
		if index < 0 && m.streamUI.hovering {
			for _, t := range l.targets {
				x := t.x - l.offset + 2
				if t.choice == m.streamUI.hover && event.X >= x && event.X < x+t.width && event.Y >= t.y+t.height+2 {
					return true
				}
			}
		}
		m.streamUI.hovering = index >= 0
		if index >= 0 {
			m.streamUI.hover = l.targets[index].choice
		}
		return true
	}
	if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
		m.mouseSelection = nil
		if event.Y == 4 && ((event.X == 1 && l.offset > 0) || (event.X == m.width-2 && l.offset+base.width < l.width)) {
			if !m.streamUI.rosterFocused {
				m.focusRoster(true)
			}
			delta := 3
			if event.X == 1 {
				delta = -3
			}
			m.moveStream(delta)
		} else if index >= 0 {
			choice := l.targets[index].choice
			if choice.completed {
				m.focusRoster(true)
				m.streamUI.completedFocused = true
				m.toggleCompleted()
			} else {
				m.selectStream(choice.id)
				m.focusRoster(false)
			}
		}
	}
	return true
}
