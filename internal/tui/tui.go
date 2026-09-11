// Package tui renders a conversation. Terminal concerns stay out of the agent,
// provider, and controller packages.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

// Session is the host control surface. The UI requests lifecycle changes; the
// conversation and agent loop implement them.
type Session interface {
	Root() message.ActorID
	Send(message.ActorID, string) (message.Receipt, error)
	Agents() []conversation.AgentInfo
	InspectAgent(message.ActorID, conversation.InspectOptions) (conversation.AgentInspection, error)
	PauseAgent(message.ActorID) (conversation.AgentInfo, error)
	ResumeAgent(message.ActorID) (conversation.AgentInfo, error)
	StopAgent(message.ActorID) (conversation.AgentInfo, error)
	NextEvent(context.Context) (conversation.Event, error)
}

type Options struct {
	Model    string
	Endpoint string
}

func Run(ctx context.Context, session Session, options Options) error {
	listenCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(listenCtx, cancel, session, options)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	_, err := p.Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

type received struct {
	event conversation.Event
	err   error
}

type entry struct {
	label, meta, body string
	at                time.Time
	renderWidth       int
	rendered          string
}

type model struct {
	transcript    *transcriptView
	ctx           context.Context
	cancel        context.CancelFunc
	session       Session
	options       Options
	input         textinput.Model
	viewport      viewport.Model
	entries       []entry
	working       map[message.ActorID]bool
	pending       map[message.MessageID]message.ActorID
	states        map[message.ActorID]agent.State
	history       []string
	historyIndex  int
	draft         string
	width, height int
	rootStopped   bool
	closed        bool
	quitting      bool
	spinner       spinner.Model
	now           func() time.Time
	busySince     time.Time
	lastElapsed   time.Duration
	activeTools   map[toolKey]agent.ToolActivity
	selecting     bool
	frozenCount   int
	frozenView    string
	markdown      *glamour.TermRenderer
	markdownWidth int
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "25", Dark: "111"})
	dimStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "242", Dark: "245"})
	userStyle  = lipgloss.NewStyle().Bold(true)
	errorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
)

func newModel(ctx context.Context, cancel context.CancelFunc, session Session, options Options) *model {
	input := textinput.New()
	input.Prompt = "› "
	input.PromptStyle = titleStyle
	input.PlaceholderStyle = dimStyle
	input.Placeholder = "Send a message…"
	input.CharLimit = 0
	input.Focus()
	m := &model{
		ctx: ctx, cancel: cancel, session: session, options: options,
		input: input, viewport: viewport.New(80, 17), width: 80, height: 24,
		working: make(map[message.ActorID]bool), pending: make(map[message.MessageID]message.ActorID), states: make(map[message.ActorID]agent.State),
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(stateStyle)),
		now:     time.Now, activeTools: make(map[toolKey]agent.ToolActivity),
	}
	m.resize(80, 24)
	m.add("Welcome", "Send a message to get started. You can keep typing while agents work.\nScroll to browse history · F2 to select and copy · /help for commands", true)
	return m
}

func (m *model) Init() tea.Cmd { return tea.Batch(textinput.Blink, m.spinner.Tick, m.listen()) }

func (m *model) listen() tea.Cmd {
	return func() tea.Msg {
		e, err := m.session.NextEvent(m.ctx)
		return received{event: e, err: err}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		m.refreshSelection()
		return m, nil
	case received:
		if msg.err != nil {
			m.closed = true
			m.refreshActivity()
			if !errors.Is(msg.err, context.Canceled) {
				m.add("System", "Conversation closed: "+msg.err.Error(), false)
			}
			return m, nil
		}
		m.observe(msg.event)
		return m, m.listen()
	case tea.MouseMsg:
		if m.selecting {
			return m, nil
		}
		if m.transcript != nil {
			if m.transcript.copying {
				return m, nil
			}
			if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp && !msg.Shift && m.transcript.viewport.AtTop() {
				m.earlierTranscript()
			}
			var cmd tea.Cmd
			m.transcript.viewport, cmd = m.transcript.viewport.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if m.transcript != nil {
			return m.transcriptKey(msg)
		}
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			return m.quit()
		case "f2":
			m.toggleSelection()
			if m.selecting {
				return m, tea.DisableMouse
			}
			return m, tea.EnableMouseCellMotion
		case "enter":
			if m.selecting {
				return m, nil
			}
			return m.submit()
		case "up":
			if m.selecting {
				return m, nil
			}
			m.recall(-1)
			return m, nil
		case "down":
			if m.selecting {
				return m, nil
			}
			m.recall(1)
			return m, nil
		case "pgup", "pgdown":
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			m.refreshSelection()
			return m, cmd
		case "ctrl+home":
			m.viewport.GotoTop()
			m.refreshSelection()
			return m, nil
		case "ctrl+end":
			m.viewport.GotoBottom()
			m.refreshSelection()
			return m, nil
		}
	}
	if m.selecting {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) submit() (tea.Model, tea.Cmd) {
	defer m.refreshActivity()
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		m.input.Reset()
		fields := strings.Fields(text)
		switch fields[0] {
		case "/quit", "/exit":
			return m.quit()
		case "/help":
			m.add("Help", "/agents  Show agents and their status\n/inspect [id]  Inspect agent state\n/transcript [id]  Browse an agent conversation\n/pause [id]    Pause at an operation boundary\n/resume [id]   Resume a paused agent\n/stop [id]     Stop an agent permanently\nIDs default to the root.\n/clear   Clear the screen; keep the conversation\n/quit    Cancel all agents and exit\n\nEnter sends · ↑/↓ input history · PgUp/PgDn scroll · Ctrl+C or Ctrl+D exits\nConsecutive tool calls share a line, grouped by agent with repeat counts. Messages render Markdown. Idle means agents are waiting; queued counts refer to pending messages.\nScroll with the mouse, trackpad, or PgUp/PgDn. Ctrl+End returns to the latest output.\nF2 freezes the display and releases the mouse for selection; agents keep running.\nDrag to select, then use your terminal Copy shortcut. F2 resumes scrolling.", true)
		case "/clear":
			m.entries = nil
			m.renderTranscript(true)
		case "/transcript":
			if len(fields) > 2 {
				m.add("Error", "Usage: /transcript [agent-id]", true)
				break
			}
			id := m.session.Root()
			if len(fields) == 2 && fields[1] != "root" {
				id = message.ActorID(fields[1])
			}
			m.openTranscript(id)
		case "/inspect", "/pause", "/resume", "/stop":
			if len(fields) > 2 {
				m.add("Error", "Usage: "+fields[0]+" [agent-id]", true)
				break
			}
			id := m.session.Root()
			if len(fields) == 2 && fields[1] != "root" {
				id = message.ActorID(fields[1])
			}
			operation := func(id message.ActorID) (conversation.AgentInfo, error) {
				inspection, err := m.session.InspectAgent(id, conversation.InspectOptions{})
				return inspection.AgentInfo, err
			}
			switch fields[0] {
			case "/pause":
				operation = m.session.PauseAgent
			case "/resume":
				operation = m.session.ResumeAgent
			case "/stop":
				operation = m.session.StopAgent
			}
			info, err := operation(id)
			if err != nil {
				m.add("Error", err.Error(), true)
			} else {
				m.add("Agent", fmt.Sprintf("%s · %s · parent %s", info.ID, info.State, info.Parent), true)
			}
		case "/agents":
			var lines []string
			for _, a := range m.session.Agents() {
				status := string(a.State)
				role := ""
				if a.ID == m.session.Root() {
					role = " (root)"
				}
				lines = append(lines, fmt.Sprintf("%s%s · %s · parent %s", a.ID, role, status, a.Parent))
			}
			m.add("Agents", strings.Join(lines, "\n"), true)
		default:
			m.add("System", "Unknown command. Type /help.", true)
		}
		return m, nil
	}
	if m.closed || m.rootStopped {
		m.add("System", "The root has stopped. Exit and restart strap to begin a new conversation.", true)
		return m, nil
	}
	receipt, err := m.session.Send(m.session.Root(), text)
	if err != nil {
		m.add("Error", err.Error(), true)
		return m, nil
	}
	m.pending[receipt.MessageID] = receipt.Recipient
	m.history = append(m.history, text)
	m.historyIndex = len(m.history)
	m.draft = ""
	m.input.Reset()
	m.addDetail("You", fmt.Sprintf("user → %s · %s", m.session.Root(), receipt.MessageID), text, true)
	return m, nil
}

func (m *model) observe(event conversation.Event) {
	defer m.refreshActivity()
	switch e := event.(type) {
	case conversation.WorkEvent:
		change := e.Event
		title := string(change.Kind)
		meta := string(change.Work.ID)
		body := change.Work.Task + " · " + string(change.Work.State)
		if change.Plan != nil {
			meta = string(change.Plan.ID)
			body = change.Plan.Title
			change.Steps = change.Plan.Steps
		}
		for _, step := range change.Steps {
			body += "\n" + string(step.Status) + " · " + step.Title
		}
		if change.Work.Blocker != "" {
			body += "\nBlocked: " + change.Work.Blocker
		}
		m.addDetail(title, meta, body, false)
	case conversation.ToolEvent:
		m.toolEvent(e)
	case conversation.AgentStateChanged:
		m.states[e.Agent] = e.State
		switch e.State {
		case agent.Running, agent.PauseRequested:
			m.working[e.Agent] = true
		default:
			delete(m.working, e.Agent)
		}
		if e.State == agent.PauseRequested || e.State == agent.Paused || e.State == agent.StopRequested {
			m.addDetail("State", string(e.Agent), string(e.State), false)
		}
	case conversation.AgentStarted:
		label := "Delegation"
		if e.Agent.ID == m.session.Root() {
			label = "Agent"
		}
		m.addDetail(label, fmt.Sprintf("%s → %s", e.Agent.Parent, e.Agent.ID), "Agent created · "+string(e.Agent.State), false)
	case conversation.AckEvent:
		if e.Receipt.Status == message.Queued && e.Receipt.Recipient != message.User {
			m.pending[e.Receipt.MessageID] = e.Receipt.Recipient
		}
		if e.Receipt.Status == message.Consumed {
			delete(m.pending, e.Receipt.MessageID)
			m.working[e.Receipt.Recipient] = true
		}
		if e.Receipt.Status == message.Undelivered {
			delete(m.pending, e.Receipt.MessageID)
			m.add("Error", fmt.Sprintf("%s was not consumed: %s", e.Receipt.MessageID, e.Receipt.Detail), false)
		}
	case conversation.MessageEvent:
		msg := e.Message
		// Keep the timer active across a reply-to-inbox handoff. Delivery
		// is pending until consumed, even if its sender has finished.
		if msg.To != message.User {
			m.pending[msg.ID] = msg.To
		}
		if msg.Kind == message.Reply {
			delete(m.working, msg.From)
		}
		if msg.From == message.User {
			return
		} // The submit path already displayed this input.
		meta := fmt.Sprintf("%s → %s · %s · %s", msg.From, msg.To, msg.Kind, msg.ID)
		if msg.ReplyTo != "" {
			meta += " · reply to " + string(msg.ReplyTo)
		}
		if msg.Event != nil {
			return
		} // Already rendered from the workflow event.
		label, body := "Message", msg.Content
		if msg.Work != nil {
			label = "Work"
			body = "Task: " + msg.Work.Task
			if msg.Work.Context != "" {
				body += "\nContext: " + msg.Work.Context
			}
			if msg.Work.ExpectedOutput != "" {
				body += "\nExpected output: " + msg.Work.ExpectedOutput
			}
		} else if msg.Kind == message.Failure {
			label = "Error"
		} else if msg.To == message.User && msg.From == m.session.Root() {
			label = "Strap"
		}
		m.addDetail(label, meta, body, false)
	case conversation.AgentExited:
		m.states[e.Agent] = agent.Stopped
		delete(m.working, e.Agent)
		for key := range m.activeTools {
			if key.agent == e.Agent {
				delete(m.activeTools, key)
			}
		}
		if e.Agent == m.session.Root() {
			m.rootStopped = true
		}
		if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
			m.add("Error", fmt.Sprintf("%s stopped: %v", e.Agent, e.Err), false)
		} else {
			m.add("Agent", fmt.Sprintf("%s stopped", e.Agent), false)
		}
	}
}

func (m *model) recall(direction int) {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex == len(m.history) {
		m.draft = m.input.Value()
	}
	m.historyIndex = max(0, min(len(m.history), m.historyIndex+direction))
	if m.historyIndex == len(m.history) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.history[m.historyIndex])
	}
	m.input.CursorEnd()
}

func (m *model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	m.cancel() // Release the pending event reader before leaving the UI.
	return m, tea.Quit
}

func (m *model) resize(width, height int) {
	m.width, m.height = max(1, width), max(1, height)
	m.input.Width = max(1, m.width-5)
	m.viewport.Width = max(1, m.width-2)
	m.viewport.Height = max(1, m.height-6)
	m.renderTranscript(false)
	if m.transcript != nil {
		m.resizeAgentTranscript()
	}
}

func (m *model) add(label, body string, follow bool) {
	m.addDetail(label, "", body, follow)
}

func (m *model) renderTranscript(follow bool) {
	bottom := m.viewport.AtBottom()
	var transcript strings.Builder
	entries := m.entries
	if m.selecting {
		entries = entries[:min(m.frozenCount, len(entries))]
	}
	for i := 0; i < len(entries); i++ {
		e := &entries[i]
		if e.label == "Tool" {
			end := i + 1
			for end < len(entries) && entries[end].label == "Tool" {
				end++
			}
			if i > 0 {
				transcript.WriteString("\n")
			}
			row := "├─ " + toolSummary(entries[i:end])
			transcript.WriteString(toolStyle.Render(ansi.Truncate(row, max(1, m.viewport.Width-1), "…")))
			transcript.WriteString("\n")
			i = end - 1
			continue
		}
		if i > 0 {
			transcript.WriteString("\n")
		}
		style := dimStyle.Bold(true)
		if e.label == "You" {
			style = userStyle
		}
		if e.label == "Strap" {
			style = titleStyle
		}
		if e.label == "Error" {
			style = errorStyle
		}
		switch e.label {
		case "Delegation", "Work", "Message":
			style = routeStyle
		case "State", "Agent", "Agents":
			style = stateStyle
		}
		body := m.renderBody(e)
		heading := style.Render(e.label) + "  " + dimStyle.Render(e.at.Format("15:04"))
		if e.meta != "" {
			heading += "  " + dimStyle.Render(e.meta)
		}
		heading = ansi.Truncate(heading, max(1, m.viewport.Width-1), "…")
		transcript.WriteString(heading + "\n" + body + "\n")
	}
	m.viewport.SetContent(strings.TrimSuffix(transcript.String(), "\n"))
	if follow || bottom {
		m.viewport.GotoBottom()
	}
}

func (m *model) status() string {
	if m.closed {
		return "Conversation closed · /quit to exit"
	}
	if m.rootStopped {
		return "Root stopped · /quit and restart to begin again"
	}
	status := "Idle"
	if m.states[m.session.Root()] == agent.Paused {
		status = "Root paused · /resume to continue"
	} else if m.states[m.session.Root()] == agent.PauseRequested {
		status = "Root pause requested…"
	}
	if m.working[m.session.Root()] && m.states[m.session.Root()] != agent.PauseRequested {
		status = "Root processing…"
	}
	children := len(m.working)
	if m.working[m.session.Root()] {
		children--
	}
	if children > 0 {
		status += fmt.Sprintf(" · %d agent(s) processing", children)
	}
	if len(m.pending) > 0 {
		status += fmt.Sprintf(" · %d message(s) queued", len(m.pending))
	}
	return status
}

func (m *model) View() string {
	if m.quitting {
		return ""
	}
	if m.transcript != nil {
		return m.transcriptDisplay()
	}
	if m.selecting {
		return m.frozenView
	}
	return m.renderView()
}

func (m *model) renderView() string {
	if m.quitting {
		return ""
	}
	line := func(s string) string { return ansi.Truncate(" "+s, m.width, "") }
	if m.height < 8 {
		return line(m.input.View())
	}
	return strings.Join([]string{
		line(m.header()),
		"", lipgloss.NewStyle().PaddingLeft(min(1, m.width-1)).Render(m.viewport.View()),
		line(dimStyle.Render(strings.Repeat("─", max(1, m.width-2)))), line(m.input.View()),
		line(m.activityLine()),
		line(dimStyle.Render(m.footer())),
	}, "\n")
}

func (m *model) header() string {
	left := titleStyle.Render("strap") + "  " + safeText(m.options.Model)
	right := dimStyle.Render(safeText(m.options.Endpoint))
	gap := m.width - 2 - lipgloss.Width(left) - lipgloss.Width(right)
	if gap >= 4 {
		return left + strings.Repeat(" ", gap) + right
	}
	return left
}

// Remote text is content, not terminal control sequences.
func safeText(s string) string {
	s = ansi.Strip(strings.ReplaceAll(s, "\r\n", "\n"))
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func (m *model) footer() string {
	if m.selecting {
		return "DISPLAY FROZEN · drag to copy · PgUp/PgDn scroll · F2 resume"
	}
	if !m.viewport.AtBottom() {
		return fmt.Sprintf("History · %.0f%% · Ctrl+End latest · Scroll / PgUp/PgDn", m.viewport.ScrollPercent()*100)
	}
	return "Enter send · Scroll history · ↑/↓ recall · F2 copy · /help"
}
