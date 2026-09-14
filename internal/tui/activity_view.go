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
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
)

// Replaced on completion, never mutated: frozen entries retain their snapshot.
type toolDisplay struct {
	preview, arguments, result, failure string
	started, finished                   time.Time
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
	d := &toolDisplay{started: a.StartedAt, finished: a.FinishedAt}
	var args map[string]json.RawMessage
	if json.Unmarshal(a.Call.Arguments, &args) == nil {
		for _, key := range []string{"path", "url", "command", "query", "pattern", "task", "agent_id"} {
			var value string
			if json.Unmarshal(args[key], &value) == nil && strings.TrimSpace(value) != "" {
				d.preview = ansi.Truncate(inlineText(value), 160, "…")
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
		d.failure = boundedToolText(a.Err.Error(), 2048)
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
	expanded map[foldKey]bool
	parents  map[uint64]uint64
	targets  []foldTarget
	focused  bool
	selected foldKey
}

func activityActor(e *entry) message.ActorID {
	if e.label == "Tool" && e.toolInfo != nil {
		return e.tool.agent
	}
	if e.output != nil && strings.TrimSpace(e.body) == "" && !e.progress && !e.outputFailed && e.message == "" {
		return e.output.Agent
	}
	return ""
}

func (m *model) foldExpanded(group []*entry) bool {
	key := foldKey{serial: group[0].serial}
	if value, ok := m.folds.expanded[key]; ok {
		return value
	}
	for _, e := range group {
		id := e.output
		if id == nil {
			id = e.activityOutput
		}
		if id != nil {
			if collapsed, ok := m.activityCollapsed[*id]; ok {
				return !collapsed
			}
		}
	}
	return false
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

func (m *model) activityName(actor message.ActorID) string {
	if actor == m.session.Root() {
		return "Strap"
	}
	return inlineText(string(actor))
}

func (m *model) renderActivity(group []*entry, firstRow int) string {
	head := group[0]
	actor := activityActor(head)
	key := foldKey{serial: head.serial}
	expanded := m.foldExpanded(group)
	width := max(1, m.viewport.Width-1)
	m.folds.targets = append(m.folds.targets, foldTarget{key: key, row: firstRow})
	var tools []*entry
	counts := map[string]int{}
	var names []string
	active, failed := false, false
	var first, last time.Time
	for _, e := range group {
		m.folds.parents[e.serial] = head.serial
		if e.toolInfo != nil {
			tools = append(tools, e)
			if counts[e.body] == 0 {
				names = append(names, e.body)
			}
			counts[e.body]++
			d := e.toolInfo
			active = active || d.finished.IsZero()
			failed = failed || d.failure != ""
			if first.IsZero() || d.started.Before(first) {
				first = d.started
			}
			if d.finished.After(last) {
				last = d.finished
			}
		} else if !e.outputFinished {
			active = true
		}
	}
	var labels []string
	for _, name := range names {
		label := name
		if counts[name] > 1 {
			label += fmt.Sprintf(" ×%d", counts[name])
		}
		labels = append(labels, label)
	}
	title := strings.Join(labels, " · ")
	if title == "" {
		title = "Thinking"
	}
	mark, style := "✓", successStyle
	if active {
		mark, style = "●", stateStyle
	}
	if failed {
		mark, style = "!", errorStyle
	}
	meta := ""
	if len(tools) > 0 {
		meta = fmt.Sprintf(" · %d call", len(tools))
		if len(tools) != 1 {
			meta += "s"
		}
	}
	if m.streamUI.selected != actor {
		meta += " · " + m.activityName(actor)
	}
	if active {
		last = m.now()
	}
	if !first.IsZero() {
		meta += " · " + elapsed(last.Sub(first))
	}
	var lines []string
	add := func(text string) {
		for _, row := range strings.Split(ansi.Hardwrap(text, width, true), "\n") {
			lines = append(lines, ansi.Truncate(row, width, ""))
		}
	}
	// Keep the fold one line even when many different tools were called.
	meta = ansi.Truncate(meta, max(0, width-12), "…")
	title = ansi.Truncate(title, max(1, width-4-ansi.StringWidth(meta)), "…")
	nameStyle := routeStyle
	if active {
		nameStyle = stateStyle
	}
	heading := m.foldMarker(key, expanded) + " " + style.Render(mark) + " " + nameStyle.Render(title) + dimStyle.Render(meta)
	add(ansi.Truncate(heading, width, "…"))
	if !expanded && len(tools) > 0 {
		latest := tools[len(tools)-1]
		for _, e := range tools {
			if e.toolInfo.finished.IsZero() {
				latest = e
				break
			}
		}
		prefix := "Latest: "
		if latest.toolInfo.finished.IsZero() {
			prefix = "Running: "
		}
		preview := latest.body
		if latest.toolInfo.preview != "" {
			preview += " · " + latest.toolInfo.preview
		}
		add(dimStyle.Render(ansi.Truncate("  "+prefix+preview, width, "…")))
	}
	// Thinking is opt-in, including while a still-empty response is generating.
	for _, e := range group {
		if e.output != nil && e.reasoning != "" && e.reasoningExpanded && !m.activityCollapsed[*e.output] {
			add(dimStyle.Render("  Thinking\n" + indentActivity(e.reasoning, "  │ ")))
		}
		if expanded && e.output != nil && e.reasoning != "" && !e.reasoningExpanded {
			add(dimStyle.Render("  Thinking · Ctrl+T show"))
		}
	}
	if expanded {
		var previous *identity.OutputID
		for _, e := range tools {
			if e.activityOutput != nil && (previous == nil || *previous != *e.activityOutput) {
				previous = e.activityOutput
				add(dimStyle.Render(fmt.Sprintf("  │ Call %d", previous.Call)))
			}
			toolKey := foldKey{serial: e.serial, tool: true}
			open := m.folds.expanded[toolKey]
			m.folds.targets = append(m.folds.targets, foldTarget{key: toolKey, row: firstRow + len(lines), column: 2})
			d := e.toolInfo
			marker, statusStyle := "✓", successStyle
			if d.finished.IsZero() {
				marker, statusStyle = "●", stateStyle
			}
			if d.failure != "" {
				marker, statusStyle = "!", errorStyle
			}
			label := "  " + m.foldMarker(toolKey, open) + " " + statusStyle.Render(marker) + " " + routeStyle.Render(e.body)
			if d.preview != "" {
				label += " · " + d.preview
			}
			end := d.finished
			if end.IsZero() {
				end = m.now()
			}
			if !d.started.IsZero() {
				label += dimStyle.Render(" · " + elapsed(end.Sub(d.started)))
			}
			add(label)
			if open {
				add(dimStyle.Render("    Arguments\n" + indentActivity(d.arguments, "    │ ")))
				result := d.result
				if result == "" {
					result = "No text output."
					if d.finished.IsZero() {
						result = "Waiting for result…"
					}
				}
				add(dimStyle.Render("    Result\n" + indentActivity(result, "    │ ")))
				if e.tokens != nil {
					add(dimStyle.Render("    " + e.tokens.label()))
				}
			}
			if d.failure != "" {
				add(errorStyle.Render("    ! " + inlineText(d.failure)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func indentActivity(text, prefix string) string {
	return prefix + strings.ReplaceAll(text, "\n", "\n"+prefix)
}

func (m *model) toggleFold(key foldKey) {
	if m.folds.expanded == nil {
		m.folds.expanded = make(map[foldKey]bool)
	}
	value := m.folds.expanded[key]
	if !key.tool {
		for i := range m.entries {
			if m.entries[i].serial == key.serial {
				end := i + 1
				for end < len(m.entries) && activityActor(&m.entries[end]) == activityActor(&m.entries[i]) {
					end++
				}
				var group []*entry
				for j := i; j < end; j++ {
					group = append(group, &m.entries[j])
				}
				value = m.foldExpanded(group)
				break
			}
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
	x, y := event.X-1-m.sidebarWidth(), event.Y-m.transcriptTop()+m.viewport.YOffset
	if event.Y < m.transcriptTop() || event.Y >= m.transcriptTop()+m.viewport.Height {
		return false
	}
	for _, target := range m.folds.targets {
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
