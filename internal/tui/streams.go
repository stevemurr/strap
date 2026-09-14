package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// Streams are views of the same entries and event reader. Selecting a stream
// never changes the recipient, execution state, or model history.
type streamUI struct {
	selected          message.ActorID // Empty selects All activity.
	rosterFocused     bool
	order             []message.ActorID // Stable discovery order, root first.
	views             map[message.ActorID]*agentStream
	works             map[work.ID]work.Work
	workOrder         []work.ID
	nextEntry         uint64
	lines             []streamAnchor
	frozenRoster      []rosterLine
	frozenTitle       string
	frozenDetails     string
	frozenFollow      string
	completedExpanded bool
	completedFocused  bool
}

type agentStream struct {
	parent   message.ActorID
	position streamPosition
	unread   map[uint64]bool // One count per message/tool row, never per token.
	since    time.Time
	output   string
	context  *contextTokens
	err      string
}

type streamAnchor struct {
	entry uint64
	line  int
}

type streamPosition struct {
	anchor streamAnchor
	offset int
	follow bool
}

func (m *model) initStreams() {
	m.streamUI = streamUI{
		selected:          m.session.Root(),
		completedExpanded: true,
		views:             make(map[message.ActorID]*agentStream),
		works:             make(map[work.ID]work.Work),
	}
	m.ensureStream("")
	m.ensureStream(m.session.Root())
	for _, info := range m.session.Agents() {
		m.ensureStream(info.ID).parent = info.Parent
		m.states[info.ID] = info.State
	}
}

func (m *model) ensureStream(id message.ActorID) *agentStream {
	if v := m.streamUI.views[id]; v != nil {
		return v
	}
	v := &agentStream{position: streamPosition{follow: true}, unread: make(map[uint64]bool)}
	m.streamUI.views[id] = v
	if id != "" && id != message.User {
		m.streamUI.order = append(m.streamUI.order, id)
	}
	return v
}

func (e *entry) inStream(id message.ActorID) bool {
	return id == "" || len(e.actors) == 0 || slices.Contains(e.actors, id)
}

func (m *model) streamIsVisible() bool {
	return !m.selecting && m.mouseSelection == nil && m.transcript == nil &&
		!(m.streamUI.rosterFocused && m.sidebarWidth() == 0) && m.height >= 8
}

func (m *model) noteStreamEntry(e *entry) {
	for _, id := range e.actors {
		if id != message.User && id != "" {
			m.ensureStream(id)
		}
	}
	for id, v := range m.streamUI.views {
		if e.inStream(id) {
			v.unread[e.serial] = true
		}
	}
}

func (m *model) markStreamRead() {
	if !m.streamIsVisible() || !m.viewport.AtBottom() {
		return
	}
	read := m.ensureStream(m.streamUI.selected).unread
	if len(read) == 0 {
		return
	}
	// A routed message seen in the root stream is also read in its sender's
	// stream. Unrelated child activity remains unread until actually visited.
	for id, v := range m.streamUI.views {
		if id == m.streamUI.selected {
			continue
		}
		for serial := range read {
			delete(v.unread, serial)
		}
	}
	clear(read)
}

func (m *model) streamPosition() streamPosition {
	p := streamPosition{offset: m.viewport.YOffset, follow: m.viewport.AtBottom()}
	if p.offset >= 0 && p.offset < len(m.streamUI.lines) {
		p.anchor = m.streamUI.lines[p.offset]
	}
	return p
}

func (m *model) restoreStreamPosition(p streamPosition) {
	if p.follow {
		m.viewport.GotoBottom()
		return
	}
	offset := p.offset
	if p.anchor.entry != 0 {
		found := false
		for i, a := range m.streamUI.lines {
			if a.entry == p.anchor.entry {
				found = true
				offset = i
				if a.line >= p.anchor.line {
					break
				}
			}
		}
		if !found {
			if parent, ok := m.folds.parents[p.anchor.entry]; ok {
				for i, a := range m.streamUI.lines {
					if a.entry == parent {
						offset = i
						break
					}
				}
			}
		}
	}
	m.viewport.SetYOffset(offset)
}

func (m *model) selectStream(id message.ActorID) {
	m.folds.focused = false
	m.streamUI.completedFocused = false
	if m.rosterGroup(id) == "Completed" {
		m.streamUI.completedExpanded = true
	}
	if id == m.streamUI.selected {
		return
	}
	m.ensureStream(m.streamUI.selected).position = m.streamPosition()
	m.streamUI.selected = id
	p := m.ensureStream(id).position
	m.mouseSelection = nil
	m.renderTranscript(true)
	m.restoreStreamPosition(p)
	m.markStreamRead()
}

func (m *model) clearStreams() {
	m.folds = foldState{}
	m.activityCollapsed = nil
	for _, v := range m.streamUI.views {
		v.position = streamPosition{follow: true}
		clear(v.unread)
	}
	m.streamUI.lines = nil
}

func (m *model) focusRoster(focus bool) {
	if focus {
		m.folds.focused = false
	}
	m.streamUI.rosterFocused = focus
	if focus {
		m.input.Blur()
	} else {
		m.input.Focus()
	}
}

func (m *model) moveStream(delta int) {
	choices := m.rosterChoices()
	i := 0
	for j, choice := range choices {
		if choice.completed == m.streamUI.completedFocused && (choice.completed || choice.id == m.streamUI.selected) {
			i = j
			break
		}
	}
	choice := choices[max(0, min(len(choices)-1, i+delta))]
	if choice.completed {
		m.streamUI.completedFocused = true
	} else {
		m.selectStream(choice.id)
	}
}

func (m *model) streamKey(key tea.KeyMsg) bool {
	if key.String() == "f6" {
		m.focusRoster(!m.streamUI.rosterFocused)
		return true
	}
	if !m.streamUI.rosterFocused {
		return false
	}
	switch key.String() {
	case "enter":
		if m.streamUI.completedFocused {
			m.toggleCompleted()
			break
		}
		m.focusRoster(false)
	case "tab", "esc":
		m.focusRoster(false)
	case "c":
		m.toggleCompleted()
	case " ":
		if m.streamUI.completedFocused {
			m.toggleCompleted()
		}
	case "left":
		if m.streamUI.completedFocused && m.streamUI.completedExpanded {
			m.toggleCompleted()
		}
	case "right":
		if m.streamUI.completedFocused && !m.streamUI.completedExpanded {
			m.toggleCompleted()
		}
	case "up", "[":
		m.moveStream(-1)
	case "down", "]":
		m.moveStream(1)
	case "pgup":
		m.moveStream(-5)
	case "pgdown":
		m.moveStream(5)
	case "home":
		m.selectStream("")
	case "end":
		m.moveStream(len(m.rosterChoices()))
	case "ctrl+c", "ctrl+d", "ctrl+t", "f2", "ctrl+home", "ctrl+end":
		return false
	}
	return true
}

func (m *model) focusCommand(fields []string) {
	if len(fields) > 2 {
		m.add("Error", "Usage: /focus [agent-id|root|all]", true)
		return
	}
	id := m.session.Root()
	if len(fields) == 2 {
		switch fields[1] {
		case "root":
		case "all":
			id = ""
		default:
			id = message.ActorID(fields[1])
		}
	}
	if _, ok := m.streamUI.views[id]; !ok {
		m.add("Error", "Unknown agent: "+string(id)+". Press F6 to browse agents.", true)
		return
	}
	m.selectStream(id)
}

func (m *model) rememberWork(w work.Work) {
	if w.ID == "" {
		return
	}
	previous, exists := m.streamUI.works[w.ID]
	if exists && previous.Revision > w.Revision {
		return
	}
	if !exists {
		m.streamUI.workOrder = append(m.streamUI.workOrder, w.ID)
	}
	m.streamUI.works[w.ID] = w.Clone()
	if w.Assignee != "" {
		m.ensureStream(w.Assignee)
	}
}

func (m *model) streamWork(id message.ActorID) (work.Work, bool) {
	var latest *work.Work
	for i := len(m.streamUI.workOrder) - 1; i >= 0; i-- {
		w := m.streamUI.works[m.streamUI.workOrder[i]]
		if w.Assignee != id {
			continue
		}
		if !workFinished(w) {
			return w, true
		}
		if latest == nil {
			latest = &w
		}
	}
	if latest != nil {
		return *latest, true
	}
	return work.Work{}, false
}

func workFinished(w work.Work) bool {
	return w.State.Terminal()
}

func workStatus(w work.Work) string {
	switch w.State {
	case work.Delivered:
		return "delivered"
	case work.Accepted:
		return "completed"
	case work.Closed:
		return "closed"
	case work.Cancelled:
		return "cancelled"
	}
	if w.Blocker != "" {
		return "blocked"
	}
	switch w.State {
	case work.NeedsCheck:
		return "ready for review"
	case work.Checking:
		return "in review"
	case work.ChangesRequested:
		if w.ActiveRepairID == "" {
			return "awaiting repair assignment"
		}
		return "repair in progress"
	}
	if w.Kind == work.Research {
		return "researching"
	}
	if w.Kind == work.AuditWork {
		return "auditing"
	}
	if w.Kind == work.Repair {
		return "repairing"
	}
	return "implementing"
}

func (m *model) observeStreamEvent(event conversation.Event) {
	switch e := event.(type) {
	case conversation.AgentStarted:
		v := m.ensureStream(e.Agent.ID)
		v.parent = e.Agent.Parent
		if m.states[e.Agent.ID] == "" {
			m.states[e.Agent.ID] = e.Agent.State
		}
	case conversation.AgentStateChanged:
		if e.Revision > 0 && e.Revision <= m.revisions[e.Agent] {
			return
		}
		v := m.ensureStream(e.Agent)
		if e.State == agent.Running || e.State == agent.PauseRequested {
			if v.since.IsZero() {
				v.since = m.now()
			}
		} else {
			v.since = time.Time{}
			v.output = ""
		}
	case conversation.AgentEvent:
		v := m.ensureStream(e.Agent)
		switch output := e.Event.(type) {
		case agent.OutputStarted:
			v.output = "generating"
			v.err = ""
		case agent.OutputDelta:
			if output.Channel == provider.ChannelReasoning {
				v.output = "thinking"
			} else {
				v.output = "responding"
			}
		case agent.OutputFinished:
			v.output = ""
			if output.Err != nil && !errors.Is(output.Err, context.Canceled) {
				v.err = output.Err.Error()
			}
		}
	case conversation.ToolEvent:
		v := m.ensureStream(e.Agent)
		if e.Activity.Err != nil {
			v.err = e.Activity.Err.Error()
		} else if e.Activity.FinishedAt.IsZero() {
			v.err = ""
		}
	case conversation.AgentExited:
		v := m.ensureStream(e.Agent)
		v.output, v.since = "", time.Time{}
		if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
			v.err = e.Err.Error()
		}
	case conversation.MessageEvent:
		if e.Message.Work != nil {
			m.rememberWork(*e.Message.Work)
		}
	case conversation.WorkEvent:
		m.rememberWork(e.Event.Work)
		if e.Event.Change != nil {
			for _, w := range e.Event.Change.Works {
				m.rememberWork(w)
			}
		}
	case conversation.ContextTokensEvent:
		v := m.ensureStream(e.Agent)
		if v.context == nil || e.Revision >= v.context.revision {
			v.context = &contextTokens{revision: e.Revision, count: e.Count, failed: e.Error != "" || e.Count < 0}
		}
	}
}

func (m *model) streamActivity(id message.ActorID) string {
	var tools []string
	for key, activity := range m.activeTools {
		if key.agent == id {
			tools = append(tools, toolName(activity.Call.Name)+" · "+elapsed(m.now().Sub(activity.StartedAt)))
		}
	}
	slices.Sort(tools)
	if len(tools) != 0 {
		return strings.Join(tools, ", ")
	}
	if v := m.streamUI.views[id]; v != nil && v.output != "" {
		return v.output
	}
	return ""
}

func (m *model) streamRole(id message.ActorID) string {
	if id == m.session.Root() {
		return "root"
	}
	if w, ok := m.streamWork(id); ok {
		if w.Kind == work.AuditWork {
			return "auditor"
		}
		return "implementor"
	}
	return "agent"
}

func (m *model) streamState(id message.ActorID) string {
	state := m.states[id]
	if state == "" {
		state = agent.Idle
	}
	return strings.ReplaceAll(string(state), "_", " ")
}

func (m *model) streamSummary() string {
	running, attention := 0, 0
	for _, id := range m.streamUI.order {
		if m.working[id] {
			running++
		}
		if m.streamNeedsAttention(id) {
			attention++
		}
	}
	return fmt.Sprintf("%d running · %d need attention", running, attention)
}

func (m *model) streamNeedsAttention(id message.ActorID) bool {
	if v := m.streamUI.views[id]; v != nil && v.err != "" {
		return true
	}
	w, ok := m.streamWork(id)
	return ok && !workFinished(w) && (w.Blocker != "" || w.State == work.NeedsCheck || w.State == work.ChangesRequested)
}
