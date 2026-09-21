package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
)

var (
	routeStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#006F87", Dark: "#22D3EE"})
	stateStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"})
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#16803C", Dark: "#4ADE80"})
)

type toolKey struct {
	agent message.ActorID
	call  string
}

// toolName is presentation only; dispatch continues to use the original name.
func toolName(name string) string {
	words := strings.FieldsFunc(safeText(name), func(r rune) bool {
		return r == '_' || r == '-' || unicode.IsSpace(r)
	})
	for i, word := range words {
		if strings.EqualFold(word, "pdf") {
			words[i] = "PDF"
		}
	}
	name = strings.Join(words, " ")
	if name == "" {
		return "Tool"
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func (m *model) toolEvent(event conversation.ToolEvent) {
	activity := event.Activity
	key := toolKey{agent: event.Agent, call: activity.Call.ID}
	name := toolName(activity.Call.Name)
	if activity.FinishedAt.IsZero() {
		m.activeTools[key] = activity
		var output *identity.OutputID
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].output != nil && m.entries[i].output.Agent == event.Agent {
				id := *m.entries[i].output
				output = &id
				break
			}
		}
		m.addEntry(entry{actors: []message.ActorID{event.Agent}, label: "Tool", meta: string(event.Agent), body: name, activityOutput: output, tool: key, toolInfo: displayTool(activity)}, false)
		return
	}
	delete(m.activeTools, key)
	shown := false
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].label == "Tool" && m.entries[i].tool == key {
			m.entries[i].toolInfo = displayTool(activity)
			m.noteStreamEntry(&m.entries[i])
			shown = true
			break
		}
	}
	// The tool row renders its failure. Keep a fallback only when its start
	// was not observed or its row has already been pruned.
	if activity.Err != nil && !shown {
		m.addAttributed("Error", string(event.Agent), name+" failed: "+activity.Err.Error(), false, event.Agent)
	}
	if !m.selecting {
		m.renderTranscript(false)
	}
}

func (m *model) endToolActivity(actor message.ActorID, reason string) {
	for i := range m.entries {
		e := &m.entries[i]
		if e.toolInfo != nil && e.toolInfo.finished.IsZero() && (actor == "" || e.tool.agent == actor) {
			d := *e.toolInfo
			d.finished, d.failure = m.now(), reason
			e.toolInfo = &d
			delete(m.activeTools, e.tool)
		}
	}
	if !m.selecting {
		m.renderTranscript(false)
	}
}

func elapsed(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	seconds := int(duration.Seconds())
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

func (m *model) busy() bool {
	if m.closed || m.quitting {
		return false
	}
	if len(m.working) > 0 || len(m.activeTools) > 0 {
		return true
	}
	for _, recipient := range m.pending {
		state := m.states[recipient]
		if state != agent.Paused && state != agent.Interrupted && !state.Terminal() {
			return true
		}
	}
	return false
}

func (m *model) addAttributed(label, meta, body string, follow bool, actors ...message.ActorID) {
	m.addEntry(entry{actors: actors, label: label, meta: meta, body: body}, follow)
}

func (m *model) addEntry(e entry, follow bool) {
	m.streamUI.nextEntry++
	e.serial = m.streamUI.nextEntry
	e.label, e.meta, e.body = safeText(e.label), safeText(e.meta), safeText(e.body)
	m.entries = append(m.entries, e)
	m.noteStreamEntry(&m.entries[len(m.entries)-1])
	if !m.selecting {
		m.renderTranscript(follow && m.entries[len(m.entries)-1].inStream(m.streamUI.selected))
	}
	m.markStreamRead()
}

func (m *model) toggleSelection() {
	if m.selecting {
		m.selecting = false
		m.frozenView = ""
		m.frozenEntries = nil
		m.plans.frozen = nil
		m.streamUI.frozenRoster = nil
		m.streamUI.frozenStacks = nil
		m.streamUI.frozenPeek = nil
		m.streamUI.frozenFollow = ""
		m.renderTranscript(false)
	} else {
		m.plans.frozen = m.planLines(m.viewport.Width, m.planBudget())
		m.streamUI.frozenStacks = m.stackBar()
		m.streamUI.frozenPeek = m.stackPeek()
		m.streamUI.frozenRoster = m.rosterLines(m.rosterHeight(), rosterColumns)
		m.streamUI.frozenFollow = m.streamFollowLabel()
		m.selecting = true
		m.frozenEntries = append([]entry(nil), m.entries...)
		m.frozenView = m.renderView()
	}
}

func (m *model) refreshSelection() {
	if m.selecting {
		m.frozenView = m.renderView()
	}
}
