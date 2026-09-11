package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

var (
	toolStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	routeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	stateStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
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
		m.addDetail("Tool", string(event.Agent), name, false)
		return
	}
	delete(m.activeTools, key)
	if activity.Err != nil {
		m.addDetail("Error", string(event.Agent), name+" failed: "+activity.Err.Error(), false)
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
	m.entries = append(m.entries, entry{label: safeText(label), meta: safeText(meta), body: safeText(body), at: m.now()})
	if !m.selecting {
		m.renderTranscript(follow)
	}
}

func (m *model) toggleSelection() {
	if m.selecting {
		m.selecting = false
		m.frozenView = ""
		m.renderTranscript(false)
	} else {
		m.selecting = true
		m.frozenCount = len(m.entries)
		m.frozenView = m.renderView()
	}
}

func (m *model) refreshSelection() {
	if m.selecting {
		m.frozenView = m.renderView()
	}
}

// toolSummary groups only an uninterrupted run of tool entries. The original
// entries remain immutable, including while the display is frozen for copying.
func toolSummary(entries []entry) string {
	type group struct {
		agent  string
		names  []string
		counts map[string]int
	}
	var groups []group
	agents := make(map[string]int)
	for _, e := range entries {
		index, exists := agents[e.meta]
		if !exists {
			index = len(groups)
			agents[e.meta] = index
			groups = append(groups, group{agent: e.meta, counts: make(map[string]int)})
		}
		g := &groups[index]
		if g.counts[e.body] == 0 {
			g.names = append(g.names, e.body)
		}
		g.counts[e.body]++
	}
	var labels []string
	for _, g := range groups {
		var calls []string
		for _, name := range g.names {
			label := name
			if g.counts[name] > 1 {
				label += fmt.Sprintf(" ×%d", g.counts[name])
			}
			calls = append(calls, label)
		}
		labels = append(labels, strings.Join(strings.Fields(g.agent), " ")+" · "+strings.Join(calls, ", "))
	}
	return strings.Join(labels, "; ")
}
