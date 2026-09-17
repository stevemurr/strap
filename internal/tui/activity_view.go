package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
)

// Replaced on completion, never mutated: frozen entries retain their snapshot.
type toolDisplay struct {
	name, preview, arguments, result, failure, notice string
	started, finished                                 time.Time
}

func boundedToolText(text string, limit int) string {
	text = safeText(text)
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "\n… output truncated · /transcript for full model history"
}

func displayTool(a agent.ToolActivity) *toolDisplay {
	d := &toolDisplay{name: a.Call.Name, started: a.StartedAt, finished: a.FinishedAt}
	var args map[string]json.RawMessage
	if json.Unmarshal(a.Call.Arguments, &args) == nil {
		for _, key := range []string{"path", "url", "command", "query", "pattern", "task", "agent_id"} {
			var value string
			if json.Unmarshal(args[key], &value) == nil && strings.TrimSpace(value) != "" {
				if d.name == "shell" && key == "command" {
					d.preview = boundedToolText(value, 8192)
				} else {
					d.preview = boundedToolText(inlineText(value), 8192)
				}
				break
			}
		}
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, a.Call.Arguments, "", "  ") == nil {
		d.arguments = boundedToolText(pretty.String(), 8192)
	} else {
		d.arguments = boundedToolText(string(a.Call.Arguments), 8192)
	}
	d.result = boundedToolText(a.Result.Content.Text(), 32768)
	d.nativeOutput(a.Result.Content.Text())
	images := 0
	for _, part := range a.Result.Content {
		if part.Image != nil {
			images++
		}
	}
	if images > 0 {
		d.result += fmt.Sprintf("\n[%d image(s); binary data omitted]", images)
	}
	if a.Err != nil {
		d.failure = boundedToolText(strings.TrimPrefix(d.failure+" · "+a.Err.Error(), " · "), 2048)
	}
	return d
}

type foldKey struct {
	serial uint64
	tool   bool
}
type foldTarget struct {
	key         foldKey
	row, column int
}
type foldState struct {
	allExpanded bool
	expanded    map[foldKey]bool
	targets     []foldTarget
	hints       []foldTarget
	focused     bool
	selected    foldKey
}

func (m *model) foldMarker(key foldKey, expanded bool) string {
	marker := "▸"
	if expanded {
		marker = "▾"
	}
	if m.folds.focused && m.folds.selected == key {
		return titleStyle.Reverse(true).Render(marker)
	}
	return dimStyle.Render(marker)
}

func (m *model) renderTool(e *entry, firstRow int) string {
	var lines []string
	width := max(1, m.viewport.Width-1)
	add := func(text string) {
		for _, row := range strings.Split(ansi.Hardwrap(text, width, true), "\n") {
			lines = append(lines, ansi.Truncate(row, width, ""))
		}
	}
	d := e.toolInfo
	key := foldKey{serial: e.serial, tool: true}
	open := m.toolExpanded(e)
	row := firstRow + len(lines)
	m.folds.targets = append(m.folds.targets, foldTarget{key: key, row: row})
	m.badges.targets = append(m.badges.targets, agentBadgeTarget{id: e.tool.agent, row: row, column: 2})
	mark, style := "•", stackIdentity(e.tool.agent)
	if d.finished.IsZero() {
		mark = "◦"
	}
	if d.failure != "" {
		mark, style = "!", errorStyle
	}
	if m.folds.focused && m.folds.selected == key {
		mark = m.foldMarker(key, open)
	}
	verb := toolName(d.name)
	switch d.name {
	case "shell":
		verb = "Ran"
		if d.finished.IsZero() {
			verb = "Running"
		}
	case "read_file", "read_pdf":
		verb = "Read"
	case "write_file", "edit_file", "apply_patch":
		verb = "Edited"
	case "list_directory", "list_files":
		verb = "Listed"
	case "search", "search_files", "web_search":
		verb = "Searched"
	}
	add(style.Render(mark) + " " + agentIcon(e.tool.agent) + " " + verb + " " + routeStyle.Render(d.preview))
	resultRows := e.toolResultRows(max(1, width-4))
	if open {
		if d.name != "shell" && d.arguments != "" && d.arguments != "{}" {
			add(dimStyle.Render("  Arguments\n" + indentActivity(d.arguments, "  │ ")))
		}
		add(dimStyle.Render(indentActivity(strings.Join(resultRows, "\n"), "  │ ")))
		if e.tokens != nil {
			add(dimStyle.Render("  " + e.tokens.label()))
		}
	} else {
		visible := resultRows
		if len(visible) > 2 {
			visible = visible[:2]
		}
		for i, line := range visible {
			prefix := "    "
			if i == 0 {
				prefix = "  └ "
			}
			add(dimStyle.Render(prefix + line))
		}
	}
	if len(resultRows) > 2 || open {
		hint := fmt.Sprintf("… +%d lines (ctrl+t to expand)", len(resultRows)-2)
		if open {
			hint = "collapse output (ctrl+t)"
		}
		m.folds.hints = append(m.folds.hints, foldTarget{key: key, row: firstRow + len(lines), column: 2})
		add(dimStyle.Render("  " + hint))
	}
	if d.notice != "" {
		add(dimStyle.Render("  " + d.notice))
	}
	if d.failure != "" {
		add(errorStyle.Render("  ! " + inlineText(d.failure)))
	}
	return strings.Join(lines, "\n")
}

func (m *model) toolExpanded(e *entry) bool {
	if open, ok := m.folds.expanded[foldKey{serial: e.serial, tool: true}]; ok {
		return open
	}
	if e.activityOutput != nil {
		if collapsed, ok := m.activityCollapsed[*e.activityOutput]; ok {
			return !collapsed
		}
	}
	return m.folds.allExpanded
}

func (m *model) toggleToolOutput() {
	m.folds.allExpanded = !m.folds.allExpanded
	clear(m.folds.expanded)
	clear(m.activityCollapsed)
	position := m.streamPosition()
	m.renderTranscript(false)
	m.restoreStreamPosition(position)
}

func indentActivity(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

func (m *model) toggleFold(key foldKey) {
	if m.folds.expanded == nil {
		m.folds.expanded = make(map[foldKey]bool)
	}
	value := m.folds.allExpanded
	for i := range m.entries {
		if m.entries[i].serial == key.serial {
			value = m.toolExpanded(&m.entries[i])
			break
		}
	}

	p := m.streamPosition()
	p.follow = false
	m.folds.expanded[key] = !value
	m.renderTranscript(false)
	m.restoreStreamPosition(p)
}

func (m *model) foldMouse(event tea.MouseMsg) bool {
	if !m.streamIsVisible() || event.Action != tea.MouseActionPress || event.Button != tea.MouseButtonLeft {
		return false
	}
	x, y := event.X-1, event.Y-m.transcriptTop()+m.viewport.YOffset
	if event.Y < m.transcriptTop() || event.Y >= m.transcriptTop()+m.viewport.Height {
		return false
	}
	for _, target := range append(append([]foldTarget{}, m.folds.targets...), m.folds.hints...) {
		if y == target.row && x >= target.column && x < target.column+2 {
			m.toggleFold(target.key)
			return true
		}
	}
	return false
}

func (m *model) foldKey(key string) bool {
	if key == "f7" {
		m.folds.focused = !m.folds.focused
		if m.folds.focused {
			m.focusRoster(false)
			m.input.Blur()
			if len(m.folds.targets) > 0 {
				m.folds.selected = m.folds.targets[len(m.folds.targets)-1].key
			}
		} else {
			m.input.Focus()
		}
		m.renderTranscript(false)
		if m.folds.focused && len(m.folds.targets) > 0 {
			row := m.folds.targets[len(m.folds.targets)-1].row
			if row < m.viewport.YOffset || row >= m.viewport.YOffset+m.viewport.Height {
				m.viewport.SetYOffset(row)
			}
		}
		return true
	}
	if !m.folds.focused {
		return false
	}
	switch key {
	case "esc", "tab":
		m.folds.focused = false
		m.input.Focus()
	case "enter", " ":
		m.toggleFold(m.folds.selected)
	case "up", "down", "home", "end":
		index := 0
		for i, target := range m.folds.targets {
			if target.key == m.folds.selected {
				index = i
			}
		}
		switch key {
		case "up":
			index--
		case "down":
			index++
		case "home":
			index = 0
		case "end":
			index = len(m.folds.targets) - 1
		}
		if len(m.folds.targets) > 0 {
			target := m.folds.targets[max(0, min(index, len(m.folds.targets)-1))]
			m.folds.selected = target.key
			if target.row < m.viewport.YOffset {
				m.viewport.SetYOffset(target.row)
			}
			if target.row >= m.viewport.YOffset+m.viewport.Height {
				m.viewport.SetYOffset(target.row - m.viewport.Height + 1)
			}
		}
	case "ctrl+c", "ctrl+d", "ctrl+t", "f2", "pgup", "pgdown", "ctrl+home", "ctrl+end":
		return false
	}
	m.renderTranscript(false)
	return true
}
