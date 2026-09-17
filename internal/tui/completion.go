package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

type slashCommand struct {
	name, description string
}

var slashCommands = []slashCommand{
	{"/activity", "Toggle response detail · agent-id/response-number"},
	{"/agents", "List agents"},
	{"/clear", "Clear the display"},
	{"/exit", "Exit Strap"},
	{"/focus", "Watch a live stream · [id|all]"},
	{"/help", "Show commands"},
	{"/inspect", "Inspect an agent · [id]"},
	{"/pause", "Pause an agent · [id]"},
	{"/quit", "Exit Strap"},
	{"/resume", "Resume an agent · [id]"},
	{"/stop", "Stop current work; keep the conversation"},
	{"/terminate", "Permanently stop an agent · [id]"},
	{"/transcript", "Browse agent history · [id]"},
}

type completionState struct {
	query     string
	selected  int
	dismissed bool
}

func (m *model) completionMatches() []slashCommand {
	text := m.input.Value()
	if m.streamUI.rosterFocused || m.completion.dismissed || !strings.HasPrefix(text, "/") ||
		strings.ContainsFunc(text, unicode.IsSpace) || m.input.LineInfo().StartColumn+m.input.LineInfo().ColumnOffset != utf8.RuneCountInString(text) {
		return nil
	}
	var matches []slashCommand
	for _, command := range slashCommands {
		if strings.HasPrefix(command.name, text) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m *model) completionHeight() int {
	return min(5, len(m.completionMatches()), max(0, m.height-6-m.input.Height()-m.streamChrome()-m.stackBarHeight()))
}

func (m *model) syncCompletion() {
	if m.completion.query != m.input.Value() {
		m.completion = completionState{query: m.input.Value()}
	}
	m.completion.selected = min(m.completion.selected, max(0, len(m.completionMatches())-1))
	m.syncInputHeight()
	height := max(1, m.height-4-m.input.Height()-m.completionHeight()-m.streamChrome()-m.stackBarHeight())
	if m.viewport.Height != height {
		position := m.streamPosition()
		m.viewport.Height = height
		m.renderTranscript(position.follow)
		m.restoreStreamPosition(position)
	}
}

// Completion only edits the draft. An exact command goes through the usual
// Enter handler, so selecting a partial /q never quits the app immediately.
func (m *model) completionKey(key string) bool {
	matches := m.completionMatches()
	if len(matches) == 0 {
		return false
	}
	switch key {
	case "up", "down":
		if m.completionHeight() == 0 {
			return false
		}
		delta := 1
		if key == "up" {
			delta = -1
		}
		m.completion.selected = (m.completion.selected + delta + len(matches)) % len(matches)
	case "tab":
		m.input.SetValue(matches[m.completion.selected].name + " ")
		m.input.CursorEnd()
	case "enter":
		for _, command := range matches {
			if command.name == m.input.Value() {
				return false
			}
		}
		m.input.SetValue(matches[m.completion.selected].name + " ")
		m.input.CursorEnd()
	case "esc":
		m.completion.dismissed = true
	default:
		return false
	}
	return true
}

func (m *model) completionView() []string {
	matches := m.completionMatches()
	height := m.completionHeight()
	if height == 0 {
		return nil
	}
	start := max(0, m.completion.selected-height+1)
	var lines []string
	for i := start; i < min(start+height, len(matches)); i++ {
		command := matches[i]
		label := "  " + command.name + "  " + command.description
		style := dimStyle
		if i == m.completion.selected {
			label = "› " + command.name + "  " + command.description
			style = titleStyle
		}
		lines = append(lines, style.Render(ansi.Truncate(label, m.viewport.Width, "…")))
	}
	return lines
}
