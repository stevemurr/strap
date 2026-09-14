package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

var (
	toolStyle    = dimStyle
	routeStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "30", Dark: "116"})
	stateStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "136", Dark: "179"})
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "114"})
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
		m.streamUI.nextEntry++
		m.entries = append(m.entries, entry{serial: m.streamUI.nextEntry, actors: []message.ActorID{event.Agent}, label: "Tool", meta: safeText(string(event.Agent)), body: name, at: m.now(), tool: key, toolInfo: displayTool(activity)})
		for i := len(m.entries) - 2; i >= 0; i-- {
			if m.entries[i].output != nil && m.entries[i].output.Agent == event.Agent {
				id := *m.entries[i].output
				m.entries[len(m.entries)-1].activityOutput = &id
				break
			}
		}
		m.noteStreamEntry(&m.entries[len(m.entries)-1])
		if !m.selecting {
			m.renderTranscript(false)
		}
		return
	}
	delete(m.activeTools, key)
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].label == "Tool" && m.entries[i].tool == key {
			m.entries[i].toolInfo = displayTool(activity)
			m.noteStreamEntry(&m.entries[i])
			break
		}
	}
	if activity.Err != nil {
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
		if state != agent.Paused && !state.Terminal() {
			return true
		}
	}
	return false
}
func (m *model) refreshActivity() {
	if m.busy() {
		if m.busySince.IsZero() {
			m.busySince = m.now()
			m.lastElapsed = 0
		}
	} else if !m.busySince.IsZero() {
		m.lastElapsed = m.now().Sub(m.busySince)
		m.busySince = time.Time{}
	}
}

func (m *model) activityLine() string {
	if !m.busy() {
		line := m.status()
		if m.lastElapsed > 0 {
			line += " · last active " + elapsed(m.lastElapsed)
		}
		return dimStyle.Render(line)
	}
	duration := m.now().Sub(m.busySince)
	var operations []string
	for key, activity := range m.activeTools {
		operations = append(operations, fmt.Sprintf("%s/%s %s", safeText(string(key.agent)), toolName(activity.Call.Name), elapsed(m.now().Sub(activity.StartedAt))))
	}
	sort.Strings(operations)
	detail := m.status()
	if len(operations) > 0 {
		detail += " · " + strings.Join(operations, ", ")
	}
	return m.spinner.View() + " " + stateStyle.Render(elapsed(duration)) + " · " + detail
}

func (m *model) addDetail(label, meta, body string, follow bool) {
	m.addAttributed(label, meta, body, follow)
}

func (m *model) addAttributed(label, meta, body string, follow bool, actors ...message.ActorID) {
	m.streamUI.nextEntry++
	progress := len(actors) == 1 && meta == string(actors[0])+" · progress" && (label == "Strap" || label == "Message")
	m.entries = append(m.entries, entry{serial: m.streamUI.nextEntry, actors: actors, label: safeText(label), meta: safeText(meta), body: safeText(body), at: m.now(), progress: progress})
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
		m.streamUI.frozenRoster = nil
		m.streamUI.frozenTitle, m.streamUI.frozenDetails, m.streamUI.frozenFollow = "", "", ""
		m.renderTranscript(false)
	} else {
		m.streamUI.frozenRoster = m.rosterLines(m.rosterHeight(), rosterColumns)
		m.streamUI.frozenTitle = m.streamTitle()
		m.streamUI.frozenDetails = m.streamDetails()
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

// Keep every call in order, including repeated tools and its batch measurement.
func toolRow(e *entry, width int) string {
	label := "├─ " + inlineText(e.meta) + " · " + e.body
	if e.tokens != nil {
		label += " · " + e.tokens.label()
	}
	return ansi.Hardwrap(label, width, true)
}
