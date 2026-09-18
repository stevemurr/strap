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

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// Session is the host control surface. The UI requests lifecycle changes; the
// conversation and agent loop implement them.
type Session interface {
	Interrupt(context.Context) error
	Root() message.ActorID
	Send(message.ActorID, string) (message.Receipt, error)
	Agents() []harness.AgentInfo
	InspectAgent(message.ActorID, conversation.InspectOptions) (harness.AgentInspection, error)
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
	session, detach := observeSession(session)
	defer detach()
	listenCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(listenCtx, cancel, session, options)
	p := tea.NewProgram(m, tea.WithOutput(terminalOutput(nil)), tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithContext(ctx))
	_, err := p.Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

type interrupted struct{ err error }

type received struct {
	event conversation.Event
	err   error
}

type entry struct {
	activityOutput    *identity.OutputID
	serial            uint64
	actors            []message.ActorID // Empty for local UI notices visible in every stream.
	reasoning         string
	contentStarted    bool
	progress          bool
	outputFailed      bool
	outputFinished    bool
	output            *identity.OutputID
	message           identity.MessageID
	label, meta, body string
	at                time.Time
	renderWidth       int
	rendered          string
	tool              toolKey
	toolInfo          *toolDisplay
	toolLayout        *toolOutputLayout
	tokens            *contextTokens
	agents            *agentsTable
}

type model struct {
	plans             planDock
	embedded          bool
	interrupting      bool
	folds             foldState
	badges            badgeState
	activityCollapsed map[identity.OutputID]bool
	transcript        *transcriptView
	ctx               context.Context
	cancel            context.CancelFunc
	session           Session
	options           Options
	input             textarea.Model
	viewport          viewport.Model
	entries           []entry
	working           map[message.ActorID]bool
	pending           map[message.MessageID]message.ActorID
	revisions         map[message.ActorID]uint64
	states            map[message.ActorID]agent.State
	history           []string
	historyIndex      int
	draft             string
	width, height     int
	rootStopped       bool
	closed            bool
	quitting          bool
	spinner           spinner.Model
	now               func() time.Time
	busySince         time.Time
	lastElapsed       time.Duration
	activeTools       map[toolKey]agent.ToolActivity
	selecting         bool
	frozenEntries     []entry
	frozenView        string
	markdown          *glamour.TermRenderer
	markdownWidth     int
	completion        completionState
	mouseSelection    *mouseSelection
	copyText          func(string) error
	nextTableID       uint64
	streamUI          streamUI
}

var (
	accentColor = lipgloss.AdaptiveColor{Light: "#2563EB", Dark: "#60A5FA"}
	accentStyle = lipgloss.NewStyle().Foreground(accentColor)
	titleStyle  = accentStyle.Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "241", Dark: "247"})
	userStyle   = lipgloss.NewStyle().Bold(true).AlignHorizontal(lipgloss.Left)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
)

func newModel(ctx context.Context, cancel context.CancelFunc, session Session, options Options) *model {
	input := newInput()
	m := &model{
		ctx: ctx, cancel: cancel, session: session, options: options,
		input: input, viewport: viewport.New(80, 17), width: 80, height: 24,
		working: make(map[message.ActorID]bool), pending: make(map[message.MessageID]message.ActorID), states: make(map[message.ActorID]agent.State), revisions: make(map[message.ActorID]uint64),
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(stateStyle)),
		now:     time.Now, activeTools: make(map[toolKey]agent.ToolActivity),
		copyText: clipboard.WriteAll,
	}
	// Trackpads can emit many wheel events; keep each step to one text row.
	m.viewport.MouseWheelDelta = 1
	m.initStreams()
	m.resize(80, 24)
	m.addAttributed("Welcome", "", "Send a message to get started. You can keep typing while agents work.\nF6 agents · F7 activity folds · /help for commands", true, session.Root())
	return m
}

func (m *model) Init() tea.Cmd { return tea.Batch(textarea.Blink, m.spinner.Tick, m.listen()) }

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
	m.syncCompletion()
	defer m.syncCompletion()
	defer m.markStreamRead()
	switch msg := msg.(type) {
	case interrupted:
		m.interrupting = false
		if msg.err != nil {
			m.add("Error", msg.err.Error(), true)
		} else {
			m.add("System", "Stopped current work. Send a new instruction to continue.", true)
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.mouseSelection = nil
		m.resize(msg.Width, msg.Height)
		m.refreshSelection()
		return m, nil
	case reasoningLoaded:
		m.finishReasoning(msg)
		return m, nil
	case agentTableCount:
		m.finishAgentTableCount(msg)
		return m, nil
	case inputPaste:
		if !m.selecting {
			if msg.err != nil {
				m.add("Error", "Paste failed: "+msg.err.Error(), false)
			} else {
				m.input.InsertString(normalizeInput(msg.text))
			}
		}
		return m, nil
	case clipboardResult:
		if m.mouseSelection == msg.selection {
			if msg.err != nil {
				m.mouseSelection.status = "Copy failed: " + safeText(msg.err.Error()) + " · F2 for terminal selection"
			} else {
				m.mouseSelection.status = "Copied to clipboard · Esc or scroll to resume"
			}
		}
		return m, nil
	case received:
		if msg.err != nil {
			m.closed = true
			m.endToolActivity("", "Event observation stopped before the result arrived")
			for id := range m.working {
				delete(m.working, id)
			}
			for id := range m.pending {
				delete(m.pending, id)
			}
			m.refreshActivity()
			if !errors.Is(msg.err, context.Canceled) {
				m.add("System", "Event observation stopped: "+msg.err.Error()+".", false)
			}
			return m, nil
		}
		m.observe(msg.event)
		return m, m.listen()
	case tea.MouseMsg:
		if m.selecting {
			return m, nil
		}
		if m.transcript != nil && m.transcript.copying {
			return m, nil
		}
		if m.transcript == nil && m.badgeMouse(msg, 1, m.transcriptTop(), m.width, m.planTop()) {
			return m, nil
		}
		if m.transcript == nil && m.streamMouse(msg) {
			return m, nil
		}
		if m.transcript == nil {
			if m.planMouse(msg, 1, m.planTop(), m.viewport.Width, m.planBudget()) {
				return m, nil
			}
			if handled, cmd := m.composerMouse(msg); handled {
				return m, cmd
			}
			if m.foldMouse(msg) {
				return m, nil
			}
		}
		if tea.MouseEvent(msg).IsWheel() {
			m.mouseSelection = nil
		} else if handled, cmd := m.selectWithMouse(msg); handled {
			return m, cmd
		}
		if m.transcript != nil {
			if m.transcript.copying {
				return m, nil
			}
			if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp && !msg.Shift && m.transcript.viewport.AtTop() {
				if m.transcript.reasoning {
					if m.transcript.reasoningEarlier && len(m.transcript.reasoningOutputs) > 0 {
						return m, m.loadReasoning(m.transcript.reasoningOutputs[0].Output.ID.Call)
					}
				} else {
					m.earlierTranscript()
				}
			}
			var cmd tea.Cmd
			m.transcript.viewport, cmd = m.transcript.viewport.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if msg.String() == "esc" && m.badges.peek != nil {
			m.badges.peek = nil
			return m, nil
		}
		m.badges.peek = nil
		if m.mouseSelection != nil {
			if msg.String() == "ctrl+c" && m.mouseSelection.text() != "" {
				return m, m.copySelection()
			}
			m.mouseSelection = nil
			if msg.String() == "esc" {
				return m, nil
			}
		}
		if m.transcript != nil {
			return m.transcriptKey(msg)
		}
		if !m.selecting && m.planKey(msg.String()) {
			return m, textarea.Blink
		}
		if !m.selecting && m.streamKey(msg) {
			return m, textarea.Blink
		}
		if !m.selecting && m.foldKey(msg.String()) {
			return m, textarea.Blink
		}
		if !m.selecting && m.completionKey(msg.String()) {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			// Menus, previews, and selection consume Escape above. Only the
			// live conversation treats it as a request to stop current work.
			if !m.selecting && m.busy() {
				return m, m.interruptWork()
			}
			return m, nil
		case "ctrl+c", "ctrl+d":
			return m.quit()
		case "ctrl+t":
			if !m.selecting {
				m.toggleToolOutput()
			}
			return m, nil
		case "f2":
			m.toggleSelection()
			if m.selecting {
				return m, tea.DisableMouse
			}
			return m, tea.EnableMouseAllMotion
		case "enter":
			if m.selecting {
				return m, nil
			}
			return m.submit()
		case "alt+up", "alt+down":
			if m.selecting {
				return m, nil
			}
			direction := 1
			if msg.String() == "alt+up" {
				direction = -1
			}
			m.recall(direction)
			return m, nil
		case "up", "down":
			if m.selecting {
				return m, nil
			}
			if m.input.LineCount() == 1 && m.input.LineInfo().Height == 1 {
				direction := 1
				if msg.String() == "up" {
					direction = -1
				}
				m.recall(direction)
				return m, nil
			}
		case "ctrl+v":
			if !m.selecting {
				return m, pasteInput
			}
			return m, nil
		case "tab":
			if !m.selecting {
				m.input.InsertString("    ")
			}
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
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyRunes {
		key.Runes = []rune(normalizeInput(string(key.Runes)))
		msg = key
	}
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) submit() (tea.Model, tea.Cmd) {
	defer m.refreshActivity()
	text := m.input.Value()
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	if !strings.Contains(text, "\n") && strings.HasPrefix(strings.TrimSpace(text), "/") {
		m.input.Reset()
		fields := strings.Fields(text)
		switch fields[0] {
		case "/quit", "/exit":
			return m.quit()
		case "/help":
			m.add("Help", "F6 focuses the agent stacks; arrows or Tab preview an agent; Enter opens its stream and returns to the root composer. Hover to preview, click to open. Select Completed and press Enter, or press c in the stacks, to expand/collapse completed work. Small terminals use a compact agent list.\n/focus [id|all]  Watch a live agent stream (default root)\n/plan [id]    Focus the persistent plan; Ctrl+P folds it, F8 focuses steps. Up/down selects steps; Enter opens updates; [/] switches plans; Esc returns to input.\n\n/agents  Show agent state, context tokens, last output, and per-call cap\n/inspect [id]  Inspect agent state\n/transcript [id]  Browse an agent conversation\n/pause [id]    Pause at an operation boundary\n/resume [id]   Resume a paused agent\n/stop          Stop current work; keep the conversation (Esc while working)\n/terminate [id] Permanently stop an agent\nIDs default to the root.\n/clear   Clear the screen; keep the conversation\n/quit    Cancel all agents and exit\n\nType / for commands · ↑/↓ select · Tab complete · Esc dismiss. Enter completes partial commands; Enter again runs them.\nEnter or the composer ↑ sends · Alt+Enter / Ctrl+J newline · ↑/↓ move within multiline input · Alt+↑/↓ input history · Tab indents outside slash completion · PgUp/PgDn scroll · Ctrl+C or Ctrl+D exits\nCommands show their arguments and a short output preview. Ctrl+T expands or collapses output. Click a status marker or disclosure hint, or F7 then ↑/↓ and Enter, to inspect individual results. Hover or click a glider icon for agent identity and status. Esc returns to composing. Progress updates, replies, and errors stay visible. /activity agent-id/response-number toggles that response’s tool results; chronological order is preserved. Context counts are inside individual tool details. Messages render Markdown. Idle means agents are waiting; queued counts refer to pending messages.\nScroll with the mouse, trackpad, or PgUp/PgDn. Ctrl+End returns to the latest output.\nDrag to select text; release to copy to the clipboard. Esc, scrolling, or typing resumes the live view. Ctrl+C copies while text is selected.\nF2 freezes the display and releases the mouse for native terminal selection; use your terminal Copy shortcut. F2 resumes scrolling. Ctrl+T expands or collapses command output; Cmd+T requires terminal-level forwarding; /transcript then t inspects recorded reasoning.", true)
		case "/activity":
			if len(fields) != 2 {
				m.add("Help", "Use /activity agent-id/response-number", true)
			} else if err := m.toggleActivity(fields[1]); err != nil {
				m.add("Error", err.Error(), true)
			}
		case "/clear":
			m.entries = nil
			m.clearStreams()
			m.renderTranscript(true)
		case "/focus":
			m.focusCommand(fields)
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
		case "/stop":
			if len(fields) != 1 {
				m.add("Error", "Usage: /stop (all current work). Use /terminate [agent-id] for permanent termination.", true)
				break
			}
			return m, m.interruptWork()
		case "/inspect", "/pause", "/resume", "/terminate":
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
			case "/terminate":
				operation = m.session.StopAgent
			}
			info, err := operation(id)
			if err != nil {
				m.add("Error", err.Error(), true)
			} else {
				m.add("Agent", fmt.Sprintf("%s · %s · parent %s", info.ID, info.State, info.Parent), true)
			}
		case "/plan":
			m.planCommand(fields)
		case "/agents":
			return m, m.showAgents()
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
	m.addAttributed("You", fmt.Sprintf("user → %s · %s", m.session.Root(), receipt.MessageID), text, true, m.session.Root())
	m.entries[len(m.entries)-1].message = receipt.MessageID
	return m, nil
}

func (m *model) observe(event conversation.Event) {
	defer m.refreshActivity()
	defer m.markStreamRead()
	m.observeStreamEvent(event)
	switch e := event.(type) {
	case conversation.AgentEvent:
		m.observeOutput(e.Event)
	case conversation.ToolBatchEvent:
		m.observeToolBatch(e)
	case conversation.ContextTokensEvent:
		m.observeContextTokens(e)
	case conversation.DiagnosticEvent:
		if e.Level == "error" || e.Level == "warn" {
			m.add("Diagnostic", e.Message, false)
		}
	case conversation.CommentaryEvent:
		if e.Output != nil {
			if row := m.outputEntry(*e.Output); row != nil {
				row.meta = string(e.Agent) + " · progress"
				row.progress = true
				row.renderWidth = 0
				if !m.selecting {
					m.renderTranscript(false)
				}
				return
			}
		}
		label := "Message"
		if e.Agent == m.session.Root() {
			label = "Strap"
		}
		m.addAttributed(label, string(e.Agent)+" · progress", e.Content, false, e.Agent)
	case conversation.WorkEvent:
		if !m.embedded {
			defer m.syncCompletion()
		}
		if e.Event.Kind == work.WorkProgressReported {
			m.addAttributed("Progress", string(e.Event.Work.Assignee), progressBody(e.Event), false, e.Event.Work.Assignee)
			return
		}
		if e.Event.Kind == work.ResearchDelivered {
			m.addAttributed("Research", string(e.Event.Work.ID), progressBody(e.Event), false, e.Event.Work.Owner, e.Event.Work.Assignee)
			return
		}
		change := e.Event
		title := toolName(string(change.Kind))
		meta := string(change.Work.ID)
		body := change.Work.Task + " · " + workStatus(change.Work)
		actors := []message.ActorID{change.Work.Owner, change.Work.Assignee, change.Actor}
		if change.Plan != nil {
			actors = append(actors, change.Plan.Owner)
			meta = string(change.Plan.ID)
			body = change.Plan.Title
			// The current steps live in the persistent dock.
			title = "Plan updated"
			if change.Plan.Revision <= 1 {
				title = "Plan created"
			}
		}
		if change.Plan == nil {
			for _, step := range change.Steps {
				body += "\n" + string(step.Status) + " · " + step.Title
			}
		}
		if change.Work.Blocker != "" {
			body += "\nBlocked: " + change.Work.Blocker
		}
		m.addAttributed("Work", title+" · "+meta, body, false, actors...)
	case conversation.ToolEvent:
		m.toolEvent(e)
	case conversation.AgentStateChanged:
		if e.Revision > 0 && e.Revision <= m.revisions[e.Agent] {
			return
		}
		m.revisions[e.Agent] = e.Revision
		m.states[e.Agent] = e.State
		switch e.State {
		case agent.Running, agent.PauseRequested:
			m.working[e.Agent] = true
		default:
			delete(m.working, e.Agent)
		}
		if e.State == agent.PauseRequested || e.State == agent.Paused || e.State == agent.Interrupted || e.State == agent.StopRequested {
			m.addAttributed("State", string(e.Agent), string(e.State), false, e.Agent)
		}
	case conversation.AgentStarted:
		label := "Delegation"
		if e.Agent.ID == m.session.Root() {
			label = "Agent"
		}
		m.addAttributed(label, fmt.Sprintf("%s → %s", e.Agent.Parent, e.Agent.ID), "Agent created · "+string(e.Agent.State), false, e.Agent.Parent, e.Agent.ID)
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
			m.addAttributed("Error", "", fmt.Sprintf("%s was not consumed: %s", e.Receipt.MessageID, e.Receipt.Detail), false, e.Receipt.Recipient)
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
			for i := range m.entries {
				if msg.ID != "" && m.entries[i].message == msg.ID {
					m.entries[i].body = safeText(msg.Content)
					m.entries[i].renderWidth = 0
					if !m.selecting {
						m.renderTranscript(false)
					}
					return
				}
			}
			m.addAttributed("You", fmt.Sprintf("user → %s · %s", msg.To, msg.ID), msg.Content, false, msg.To)
			m.entries[len(m.entries)-1].message = msg.ID
			return
		}
		if msg.Output != nil {
			if row := m.outputEntry(*msg.Output); row != nil {
				row.progress = false
				row.meta = fmt.Sprintf("%s → %s · %s", msg.From, msg.To, msg.ID)
				row.message = msg.ID
				row.actors = []message.ActorID{msg.From, msg.To}
				m.noteStreamEntry(row)
				row.renderWidth = 0
				if !m.selecting {
					m.renderTranscript(false)
				}
				return
			}
		}
		meta := fmt.Sprintf("%s → %s · %s · %s", msg.From, msg.To, msg.Kind, msg.ID)
		if msg.ReplyTo != "" {
			meta += " · reply to " + string(msg.ReplyTo)
		}
		if msg.Event != nil {
			return
		} // Already rendered from the workflow event.
		label, body := "Message", msg.Content
		if msg.Progress != nil {
			label = "Progress notice"
			body = noticeBody(msg.Progress)
		} else if msg.Work != nil {
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
		m.addAttributed(label, meta, body, false, msg.From, msg.To)
	case conversation.AgentExited:
		m.endToolActivity(e.Agent, "Agent stopped before the result arrived")
		if !m.states[e.Agent].Terminal() {
			m.states[e.Agent] = agent.Stopped
		}
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
			m.addAttributed("Error", "", fmt.Sprintf("%s stopped: %v", e.Agent, e.Err), false, e.Agent)
		} else {
			m.addAttributed("Agent", "", fmt.Sprintf("%s stopped", e.Agent), false, e.Agent)
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

func (m *model) interruptWork() tea.Cmd {
	if m.interrupting {
		return nil
	}
	m.interrupting = true
	m.add("System", "Stopping current work…", true)
	return func() tea.Msg { return interrupted{err: m.session.Interrupt(m.ctx)} }
}

func (m *model) resize(width, height int) {
	m.badges.peek = nil
	m.width, m.height = max(1, width), max(1, height)
	// Keep two text columns internally so wide runes remain navigable even
	// when the terminal is smaller; renderView clips each displayed row.
	m.viewport.Width = max(1, m.width-2)
	m.input.SetWidth(max(4, m.viewport.Width-m.composerInset()))
	m.syncCompletion()
	m.renderTranscript(false)
	if m.transcript != nil {
		m.resizeAgentTranscript()
	}
}

func (m *model) add(label, body string, follow bool) {
	m.addDetail(label, "", body, follow)
}

func (m *model) renderTranscript(follow bool) {
	position := m.streamPosition()
	position.follow = position.follow || follow
	source := m.entries
	if m.selecting {
		source = m.frozenEntries
	}
	var rows []string
	m.streamUI.lines = nil
	block := func(e *entry, text string) {
		if len(rows) > 0 {
			rows = append(rows, "")
			m.streamUI.lines = append(m.streamUI.lines, streamAnchor{entry: e.serial, line: -1})
		}
		for i, line := range strings.Split(text, "\n") {
			rows = append(rows, line)
			m.streamUI.lines = append(m.streamUI.lines, streamAnchor{entry: e.serial, line: i})
		}
	}
	m.folds.targets = nil
	m.badges.targets = nil
	m.folds.hints = nil
	for i := 0; i < len(source); i++ {
		e := &source[i]
		if !e.inStream(m.streamUI.selected) {
			continue
		}

		firstRow := len(rows)
		if firstRow > 0 {
			firstRow++
		}
		if e.toolInfo != nil {
			block(e, m.renderTool(e, firstRow))
		} else if e.output != nil && strings.TrimSpace(e.body) == "" && !e.outputFailed && e.message == "" {
			// Retain reasoning in the event history, without a live placeholder.
			continue
		} else {
			block(e, m.renderMessage(e, firstRow))
		}
	}
	if len(rows) == 0 {
		rows = []string{dimStyle.Render("No activity in this stream yet.")}
	}
	m.viewport.SetContent(strings.Join(rows, "\n"))
	m.restoreStreamPosition(position)
}

func (m *model) status() string {
	if m.closed {
		return "Conversation closed · /quit to exit"
	}
	if m.rootStopped {
		return "Root stopped · /quit and restart to begin again"
	}
	if m.interrupting {
		return "Stopping current work…"
	}
	status := "Idle"
	if m.states[m.session.Root()] == agent.Interrupted {
		status = "Stopped · send a new instruction to continue"
	} else if m.states[m.session.Root()] == agent.Paused {
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
	if m.mouseSelection != nil {
		return m.mouseSelection.view(m.width)
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
		var rows []string
		for _, row := range strings.Split(m.input.View(), "\n") {
			rows = append(rows, line(row))
		}
		return strings.Join(rows, "\n")
	}
	lines := strings.Split(m.streamBody(), "\n")
	for _, suggestion := range m.completionView() {
		lines = append(lines, suggestion)
	}
	lines = append(lines, planText(m.planLines(m.viewport.Width, m.planBudget()))...)
	lines = append(lines, m.renderComposer()...)
	lines = append(m.stackBar(), lines...)
	for i, row := range lines {
		lines[i] = ansi.Truncate(" "+fitStreamCell(row, m.viewport.Width), m.width, "")
	}
	view := strings.Join(lines, "\n")
	if p := m.stackPeek(); p != nil {
		s := newChipSurface(m.width, m.height)
		s.paint(0, 0, view, -1)
		s.paint(p.x, p.y, p.text, -1)
		return s.String()
	}
	return m.overlayBadgePeek(view, m.width, m.height)
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
	if m.completionHeight() > 0 {
		return "↑/↓ select · Tab complete · Enter confirm · Esc dismiss"
	}
	if m.streamUI.rosterFocused {
		return "←/→ preview · Enter open · c completed · Esc compose"
	}
	if !m.viewport.AtBottom() {
		return fmt.Sprintf("History · %.0f%% · Ctrl+End latest · Scroll / PgUp/PgDn", m.viewport.ScrollPercent()*100)
	}
	return "Enter → root · F6 agents · Ctrl+T output · /help"
}
