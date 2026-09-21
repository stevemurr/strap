package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// RunEval observes the runner. Navigation changes only presentation; Ctrl+C
// cancels the run through its owner and waits for workspace/trace cleanup.
// Completion exits automatically, without waiting for terminal input.
func RunEval(ctx context.Context, opts eval.Options, input io.Reader, output io.Writer) ([]eval.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newEvalModel(ctx, cancel, opts)
	p := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(terminalOutput(output)), tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithContext(ctx), tea.WithoutSignalHandler())
	previous := opts.Observe
	opts.Observe = func(event eval.Progress) {
		if previous != nil {
			previous(event)
		}
		p.Send(event)
	}
	opts.Log = evalLogWriter{send: func(line string) { p.Send(evalLog(line)) }}
	done := make(chan evalDone, 1)
	go func() {
		results, err := eval.Run(ctx, opts)
		result := evalDone{results: results, err: err}
		done <- result
		p.Send(result)
	}()
	_, uiErr := p.Run()
	cancel()
	result := <-done
	if errors.Is(uiErr, tea.ErrProgramKilled) && result.err != nil {
		uiErr = nil
	}
	return result.results, errors.Join(result.err, uiErr)
}

type evalDone struct {
	results []eval.Result
	err     error
}

type evalLog string
type evalLogWriter struct{ send func(string) }

func (w evalLogWriter) Write(p []byte) (int, error) {
	w.send(strings.TrimSpace(string(p)))
	return len(p), nil
}

type evalProblem struct {
	task          eval.Task
	phase         eval.Phase
	started, last time.Time
	root          message.ActorID
	result        *eval.Result
	activity      *model
	tools         map[string]bool // invocation -> finished; counts each invocation once
	toolErrors    map[string]bool
	outputs       map[identity.OutputID]bool
	input, output int64
	usageCalls    int
	missingUsage  bool
	status        string
}

func (p *evalProblem) active() bool {
	return p.phase == eval.Starting || p.phase == eval.Running || p.phase == eval.Grading
}
func (p *evalProblem) runningTools() int {
	n := 0
	for _, finished := range p.tools {
		if !finished {
			n++
		}
	}
	return n
}

type evalModel struct {
	ctx                    context.Context
	cancel                 context.CancelFunc
	opts                   eval.Options
	problems               []*evalProblem
	byID                   map[string]*evalProblem
	selected               int
	width, height          int
	listOffset             int
	queueOpen, metricsOpen bool
	queueCursor            int
	spinner                spinner.Model
	started                time.Time
	now                    func() time.Time
	stopping               bool
	followActive           bool
	lastLog                string
}

func newEvalModel(ctx context.Context, cancel context.CancelFunc, opts eval.Options) *evalModel {
	return &evalModel{ctx: ctx, cancel: cancel, opts: opts, byID: map[string]*evalProblem{}, width: 100, height: 30, spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(stateStyle)), started: time.Now(), now: time.Now, followActive: true}
}
func (m *evalModel) Init() tea.Cmd { return m.spinner.Tick }
func (m *evalModel) current() *evalProblem {
	if len(m.problems) == 0 {
		return nil
	}
	return m.problems[m.selected]
}

func (m *evalModel) observe(e eval.Progress) {
	defer m.followProblem()
	p := m.byID[e.Task.ID]
	if p == nil {
		p = &evalProblem{task: e.Task, phase: eval.Queued, tools: map[string]bool{}, toolErrors: map[string]bool{}, outputs: map[identity.OutputID]bool{}}
		m.byID[e.Task.ID] = p
		m.problems = append(m.problems, p)
	}
	if e.Phase != "" {
		p.phase = e.Phase
	}
	if e.Phase == eval.Starting {
		p.started = e.At
		p.status = "Starting session"
	}
	if e.Root != "" && p.activity == nil {
		p.root = e.Root
		p.activity = newEvalActivity(m.ctx, e.Root)
		m.resizeActivity(p)
	}
	if e.Phase == eval.Running {
		p.status = "Waiting for model"
	}
	if e.Phase == eval.Grading {
		p.status = "Running hidden tests"
	}
	if e.Result != nil {
		copy := *e.Result
		p.result = &copy
		p.started = copy.StartedAt
		p.status = string(copy.Outcome)
		if p.activity == nil {
			p.activity = newEvalActivity(m.ctx, "")
			m.resizeActivity(p)
		}
		if copy.Grade != nil {
			p.activity.add("Grade", string(copy.Outcome)+"\n"+safeText(copy.Grade.Output), false)
		}
		if copy.Error != "" {
			p.activity.add("Error", safeText(copy.Error), false)
		}
		if copy.ExecutionError != "" {
			p.activity.add("Execution error", safeText(copy.ExecutionError), false)
		}
		if copy.TimedOut {
			p.activity.add("Eval", "Session budget exhausted; see submission status for grading readiness.", false)
		}
		if copy.NoReply {
			p.activity.add("Eval", "Session ended without a root reply; see submission status for grading readiness.", false)
		}
	}
	if e.Event == nil {
		return
	}
	p.last = e.At
	switch v := e.Event.(type) {
	case conversation.ToolEvent:
		key := string(v.Agent) + "/" + v.Activity.InvocationID
		if v.Activity.InvocationID == "" {
			key = string(v.Agent) + "/" + v.Activity.Call.ID
		}
		p.tools[key] = p.tools[key] || !v.Activity.FinishedAt.IsZero()
		if v.Activity.Err != nil {
			p.toolErrors[key] = true
		}
		if p.runningTools() > 0 {
			p.status = "Running tools"
		} else {
			p.status = "Waiting for model"
		}
	case conversation.AgentEvent:
		switch fact := v.Event.(type) {
		case agent.OutputStarted:
			p.outputs[fact.Output] = true
			p.status = "Waiting for model"
		case agent.OutputDelta:
			if fact.Channel == provider.ChannelReasoning {
				p.status = "Working"
			} else {
				p.status = "Responding"
			}
		}
	case conversation.MessageEvent:
		if v.Message.From == p.root && v.Message.To == message.User {
			p.status = "Waiting for session to settle"
		}
	case conversation.UsageEvent:
		p.usageCalls++
		u := v.Observation.Usage
		if u == nil || u.InputTokens == nil || u.OutputTokens == nil {
			p.missingUsage = true
		}
		if u != nil {
			if u.InputTokens != nil {
				p.input += *u.InputTokens
			}
			if u.OutputTokens != nil {
				p.output += *u.OutputTokens
			}
		}
	}
	if p.activity != nil {
		p.activity.observe(e.Event)
		if _, ok := e.Event.(conversation.WorkEvent); ok {
			m.resizeActivity(p)
		}
		trimEvalActivity(p.activity)
	}
}

func (m *evalModel) followProblem() {
	if !m.followActive || (m.current() != nil && m.current().active()) {
		return
	}
	for i, p := range m.problems {
		if p.active() {
			m.selected = i
			return
		}
	}
}

func (m *evalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case eval.Progress:
		m.observe(v)
		m.resizeActivities()
	case evalDone:
		return m, tea.Quit
	case evalLog:
		m.lastLog = safeText(string(v))
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, v.Width), max(1, v.Height)
		for _, p := range m.problems {
			m.resizeActivity(p)
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(v)
		if p := m.current(); p != nil && p.activity != nil {
			p.activity.spinner = m.spinner
		}
		return m, cmd
	case tea.KeyMsg:
		key := v.String()
		if key == "ctrl+c" || key == "ctrl+d" {
			m.stopping = true
			m.cancel()
			return m, nil
		}
		p := m.current()
		if p == nil {
			return m, nil
		}
		if key == "q" {
			m.toggleQueue()
			return m, nil
		}
		if key == "m" {
			m.metricsOpen = !m.metricsOpen
			m.resizeActivities()
			return m, nil
		}
		if m.queueOpen {
			switch key {
			case "esc":
				m.toggleQueue()
			case "up", "k", "left":
				m.queueCursor = max(0, m.queueCursor-1)
			case "down", "j", "right":
				m.queueCursor = min(len(m.problems)-1, m.queueCursor+1)
			case "home":
				m.queueCursor = 0
			case "end":
				m.queueCursor = len(m.problems) - 1
			case "pgup":
				m.queueCursor = max(0, m.queueCursor-max(1, m.queueRows()*m.queueColumns()))
			case "pgdown":
				m.queueCursor = min(len(m.problems)-1, m.queueCursor+max(1, m.queueRows()*m.queueColumns()))
			case "enter":
				m.selected = m.queueCursor
				m.followActive = false
				m.toggleQueue()
			case "f":
				m.followActive = true
				m.followProblem()
				m.toggleQueue()
			}
			return m, nil
		}
		a := p.activity
		if a != nil && key == "esc" && a.badges.peek != nil {
			a.badges.peek = nil
			return m, nil
		}
		if a != nil {
			a.badges.peek = nil
		}
		if a != nil && (key == "p" || a.planKey(key)) {
			if key == "p" {
				a.togglePlan()
			}
			m.resizeActivity(p)
			m.followActive = false
			return m, nil
		}
		if a != nil && a.foldKey(key) {
			m.followActive = false
			return m, nil
		}
		switch key {
		case "up", "k":
			m.followActive = false
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.followActive = false
			m.selected = min(len(m.problems)-1, m.selected+1)
		case "home":
			m.followActive = false
			m.selected = 0
		case "end":
			m.followActive = false
			m.selected = len(m.problems) - 1
		case "f":
			m.followActive = true
			m.followProblem()
		case "enter":
			m.followActive = false
			if a != nil {
				a.foldKey("f7")
			}
		case "ctrl+t":
			if a != nil {
				a.toggleToolOutput()
			}
		case "tab":
			m.followActive = false
			if a != nil {
				ids := append([]message.ActorID{""}, a.streamUI.order...)
				next := 0
				for i, id := range ids {
					if id == a.streamUI.selected {
						next = (i + 1) % len(ids)
						break
					}
				}
				a.selectStream(ids[next])
			}
		case "pgup", "pgdown":
			m.followActive = false
			if a != nil {
				a.viewport, _ = a.viewport.Update(v)
			}
		case "ctrl+home":
			if a != nil {
				a.viewport.GotoTop()
			}
		case "ctrl+end":
			if a != nil {
				a.viewport.GotoBottom()
			}
		}
	case tea.MouseMsg:
		p := m.current()
		if p == nil {
			return m, nil
		}
		if v.Y >= m.queueTop() && v.Y < m.queueTop()+m.queueHeight() {
			if tea.MouseEvent(v).IsWheel() {
				if !m.queueOpen {
					m.toggleQueue()
				}
				m.queueCursor = max(0, min(len(m.problems)-1, m.queueCursor+wheelStep(v.Button)))
				return m, nil
			}
			if v.Action == tea.MouseActionPress && v.Button == tea.MouseButtonLeft {
				if v.Y == m.queueTop()+1 {
					m.toggleQueue()
				} else if m.queueOpen && v.Y >= m.queueTop()+2 && v.Y < m.queueTop()+2+m.queueRows() {
					start, end := m.queueWindow()
					cell := max(1, (m.detailWidth()-2*(m.queueColumns()-1))/m.queueColumns())
					col := max(0, min(m.queueColumns()-1, (v.X-1)/(cell+2)))
					index := start + (v.Y-m.queueTop()-2)*m.queueColumns() + col
					if index < end {
						m.selected = index
						m.followActive = false
						m.toggleQueue()
					}
				}
			}
			return m, nil
		}
		if a := p.activity; a != nil {
			top := m.activityTop()
			if a.badgeMouse(v, 1, top, m.width, top+a.viewport.Height) {
				return m, nil
			}
			if a.planMouse(v, 1, top+a.viewport.Height, m.detailWidth(), m.evalPlanBudget()) {
				m.resizeActivity(p)
				return m, nil
			}
			if v.Y < top || v.Y >= top+a.viewport.Height {
				return m, nil
			}
			if tea.MouseEvent(v).IsWheel() {
				m.followActive = false
				a.viewport, _ = a.viewport.Update(v)
			} else if v.Action == tea.MouseActionPress && v.Button == tea.MouseButtonLeft {
				if key, ok := a.folds.hit(v.X-1, v.Y-top+a.viewport.YOffset); ok {
					m.followActive = false
					a.toggleFold(key)
				}
			}
		}
	}
	return m, nil
}

// All hit targets and viewport sizing use the same measurements as View.
func (m *evalModel) detailWidth() int { return max(1, m.width-2) }
func (m *evalModel) activityTop() int { return 9 }
func (m *evalModel) queueColumns() int {
	if m.width >= 100 {
		return 2
	}
	return 1
}
func (m *evalModel) queueRows() int {
	if !m.queueOpen {
		return 0
	}
	return max(0, min(3, (m.height-18)/2, (len(m.problems)+m.queueColumns()-1)/m.queueColumns()))
}
func (m *evalModel) queueHeight() int {
	if m.queueOpen {
		return 3 + m.queueRows()
	}
	return 2
}
func (m *evalModel) footerHeight() int {
	if m.metricsOpen {
		return 5
	}
	return 2
}
func (m *evalModel) queueTop() int       { return max(0, m.height-m.footerHeight()-m.queueHeight()) }
func (m *evalModel) evalPlanBudget() int { return max(0, min(14, m.queueTop()-m.activityTop()-4)) }
func (m *evalModel) resizeActivity(p *evalProblem) {
	if p.activity == nil {
		return
	}
	a := p.activity
	width := m.detailWidth()
	height := max(1, m.queueTop()-m.activityTop()-len(a.planLines(width, m.evalPlanBudget())))
	if a.viewport.Width == width && a.viewport.Height == height {
		return
	}
	position := a.streamPosition()
	a.badges.peek = nil
	a.width = width
	a.viewport.Width = width
	a.viewport.Height = height
	a.renderTranscript(false)
	a.restoreStreamPosition(position)
}
func (m *evalModel) resizeActivities() {
	for _, p := range m.problems {
		m.resizeActivity(p)
	}
}
func (m *evalModel) toggleQueue() {
	m.queueOpen = !m.queueOpen
	if m.queueOpen {
		m.queueCursor = m.selected
		if a := m.current(); a != nil && a.activity != nil {
			a.activity.plans.focused = false
			a.activity.folds.focused = false
		}
	}
	m.resizeActivities()
}
func (m *evalModel) queueWindow() (int, int) {
	capacity := m.queueRows() * m.queueColumns()
	if capacity == 0 {
		return 0, 0
	}
	m.queueCursor = max(0, min(m.queueCursor, len(m.problems)-1))
	if m.queueCursor < m.listOffset {
		m.listOffset = m.queueCursor
	}
	if m.queueCursor >= m.listOffset+capacity {
		m.listOffset = m.queueCursor - capacity + 1
	}
	m.listOffset = max(0, min(m.listOffset, max(0, len(m.problems)-capacity)))
	return m.listOffset, min(len(m.problems), m.listOffset+capacity)
}
func (m *evalModel) queueLines() []string {
	queued := 0
	var next []string
	for _, p := range m.problems {
		if p.phase == eval.Queued {
			queued++
			if len(next) < 2 {
				next = append(next, inlineText(strings.TrimPrefix(p.task.ID, p.task.Tier+"-")))
			}
		}
	}
	arrow := "▸"
	if m.queueOpen {
		arrow = "▾"
	}
	line := titleStyle.Render(fmt.Sprintf("%s Queue · %d", arrow, queued))
	if len(next) > 0 {
		line += dimStyle.Render("   Up next  " + strings.Join(next, " · "))
	} else {
		line += dimStyle.Render("   No upcoming tests")
	}
	rows := []string{dimStyle.Render(strings.Repeat("─", m.detailWidth())), line}
	if !m.queueOpen {
		return rows
	}
	start, end := m.queueWindow()
	cols := m.queueColumns()
	cell := max(1, (m.detailWidth()-2*(cols-1))/cols)
	for r := 0; r < m.queueRows(); r++ {
		var cells []string
		for c := 0; c < cols; c++ {
			i := start + r*cols + c
			label := ""
			if i < end {
				p := m.problems[i]
				mark := "○"
				if p.active() {
					mark = "●"
				}
				if p.result != nil {
					if p.result.Passed {
						mark = "✓"
					} else {
						mark = "×"
					}
				}
				label = mark + " " + inlineText(p.task.ID) + " · " + string(p.phase)
				if i == m.queueCursor {
					label = accentStyle.Render("› " + label)
				} else {
					label = dimStyle.Render("  " + label)
				}
			}
			cells = append(cells, fitStreamCell(label, cell))
		}
		rows = append(rows, strings.Join(cells, "  "))
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf("All tests %d–%d / %d · ↑↓ browse · Enter view · Esc close", min(start+1, end), end, len(m.problems))))
	return rows
}
func (m *evalModel) metrics(p *evalProblem) []string {
	contextLabel := "context tokens unavailable"
	if p.activity != nil {
		id := p.activity.streamUI.selected
		if id == "" {
			id = p.root
		}
		if c := p.activity.ensureStream(id).context; c != nil {
			contextLabel = c.label()
		}
		if id != "" {
			contextLabel += " · " + string(id)
		}
	}
	tools := fmt.Sprintf("Tools: %d · %d running · %d errors · %d model calls", len(p.tools), p.runningTools(), len(p.toolErrors), len(p.outputs))
	usage := "Tokens in/out: unavailable"
	if p.usageCalls > 0 {
		usage = fmt.Sprintf("Tokens in/out: %s / %s", tokenDigits(p.input), tokenDigits(p.output))
		if p.missingUsage {
			usage += " (partial)"
		}
	}
	return []string{contextLabel, tools, usage}
}
func (m *evalModel) View() string {
	if m.width < 2 {
		return " "
	}
	width := m.detailWidth()
	done, passed, failed, active, submitted := 0, 0, 0, 0, 0
	for _, p := range m.problems {
		if p.phase == eval.Finished {
			done++
			if p.result != nil && p.result.Outcome == eval.Submitted {
				submitted++
			} else if p.result != nil && p.result.Passed {
				passed++
			} else {
				failed++
			}
		}
		if p.active() {
			active++
		}
	}
	state := "running"
	if m.stopping {
		state = "stopping"
	}
	header := titleStyle.Render("strap / eval") + dimStyle.Render(fmt.Sprintf("   %s · %s · %s", inlineText(m.opts.Config.Model.Model), state, m.now().Sub(m.started).Round(time.Second)))
	barWidth := max(1, min(40, width))
	filled, passWidth := 0, 0
	if len(m.problems) > 0 {
		filled = barWidth * done / len(m.problems)
		passWidth = barWidth * (passed + submitted) / len(m.problems)
	}
	bar := successStyle.Render(strings.Repeat("━", passWidth)) + errorStyle.Render(strings.Repeat("━", filled-passWidth)) + dimStyle.Render(strings.Repeat("─", barWidth-filled))
	counts := fmt.Sprintf("%d / %d complete   ", done, len(m.problems)) + successStyle.Render(fmt.Sprintf("✓ %d passed", passed)) + dimStyle.Render(fmt.Sprintf(" · %d failed · %d active · %d queued", failed, active, len(m.problems)-done-active))
	if submitted > 0 {
		counts += dimStyle.Render(fmt.Sprintf(" · %d submitted", submitted))
	}
	lines := []string{header, bar, counts, ""}
	p := m.current()
	if p == nil {
		lines = append(lines, "Loading task ladder…")
	} else {
		lines = append(lines, titleStyle.Render(inlineText(p.task.Title)))
		status := m.problemStatus(p)
		if p.active() {
			status = "● " + status + " / " + p.task.SessionTimeout().String()
		}
		lines = append(lines, dimStyle.Render(inlineText(p.task.ID)+" · "+status), dimStyle.Render(strings.Repeat("─", width)))
		stream, follow := "All activity", "following latest"
		if p.activity != nil {
			if p.activity.streamUI.selected != "" {
				stream = "Activity / " + string(p.activity.streamUI.selected)
			}
			if !p.activity.viewport.AtBottom() {
				follow = "scrolled · Ctrl+End to follow"
			}
		}
		if p.phase == eval.Finished {
			follow = "recorded activity"
		} else if !p.last.IsZero() && m.now().Sub(p.last) > 30*time.Second {
			follow = "last event " + m.now().Sub(p.last).Round(time.Second).String() + " ago"
		}
		lines = append(lines, dimStyle.Render(stream+" · "+follow), "")
		if p.activity != nil {
			lines = append(lines, strings.Split(p.activity.viewport.View(), "\n")...)
			lines = append(lines, planText(p.activity.planLines(width, m.evalPlanBudget()))...)
		} else {
			lines = append(lines, dimStyle.Render("Waiting for an available worker."))
		}
	}
	for len(lines) < m.queueTop() {
		lines = append(lines, "")
	}
	lines = lines[:min(len(lines), m.queueTop())]
	lines = append(lines, m.queueLines()...)
	footer := "Q queue · ↑↓ problems · f follow · M metrics · ^P plan · F8 steps · F7 details · ^C stop"
	if m.queueOpen {
		footer = "Q / Esc close queue · ↑↓ browse · Enter view · f follow · ^C stop"
	} else if p != nil && p.activity != nil {
		if p.activity.plans.focused {
			footer = "↑↓ steps · Enter details · PgUp/Dn more · Esc back · ^C stop"
		} else if p.activity.folds.focused {
			footer = "↑↓ activity · Enter details · Esc back · ^C stop"
		}
	}
	statusLine := "READ ONLY"
	if p != nil {
		metrics := m.metrics(p)
		statusLine += " · " + metrics[0] + fmt.Sprintf(" · %d tools · %d calls", len(p.tools), len(p.outputs))
		if len(p.toolErrors) > 0 {
			statusLine += fmt.Sprintf(" · %d tool errors", len(p.toolErrors))
		}
		if m.metricsOpen {
			lines = append(lines, dimStyle.Render(metrics[1]), dimStyle.Render(metrics[2]), dimStyle.Render("Results → "+safeText(m.opts.Mounts.Results)))
		}
	}
	if p == nil && m.metricsOpen {
		lines = append(lines, "", "", "")
	}
	lines = append(lines, dimStyle.Render(statusLine), dimStyle.Render(footer))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		lines[i] = " " + ansi.Truncate(lines[i], width, "…")
	}
	view := strings.Join(lines, "\n")
	if p != nil && p.activity != nil {
		return p.activity.overlayBadgePeek(view, m.width, m.height)
	}
	return view
}

func (m *evalModel) problemStatus(p *evalProblem) string {
	if p.phase == eval.Queued {
		return "queued"
	}
	duration := m.now().Sub(p.started)
	if p.result != nil {
		duration = p.result.Duration
	}
	return p.status + " · " + duration.Round(time.Second).String()
}

// Keep a bounded live tail for every problem. Full history remains in trace.jsonl.
func trimEvalActivity(a *model) {
	// Bound each stream fragment as well as the number and combined size of
	// entries: one unusually long model response must not grow for the full run.
	bytes, keep := 0, len(a.entries)
	for i := len(a.entries) - 1; i >= 0; i-- {
		e := &a.entries[i]
		if len(e.body) > 32768 {
			e.body = evalDisplayText(e.body, 32768)
			e.renderWidth = 0
		}
		if len(e.reasoning) > 16384 {
			e.reasoning = evalDisplayText(e.reasoning, 16384)
			e.renderWidth = 0
		}
		if e.reportDetail != nil {
			if len(e.reportDetail.body) > 32768 {
				e.reportDetail.body = evalDisplayText(e.reportDetail.body, 32768)
				e.reportDetail.renderWidth = 0
			}
			bytes += len(e.reportDetail.body)
		}
		bytes += len(e.body) + len(e.reasoning)
		if e.toolInfo != nil {
			bytes += len(e.toolInfo.name) + len(e.toolInfo.preview) + len(e.toolInfo.arguments) + len(e.toolInfo.result) + len(e.toolInfo.failure) + len(e.toolInfo.notice)
		}
		if bytes > 1<<20 || len(a.entries)-i > 200 {
			keep = len(a.entries) - i - 1
			break
		}
	}
	if keep == len(a.entries) {
		return
	}
	a.entries = append([]entry(nil), a.entries[len(a.entries)-max(1, keep):]...)
	first := a.entries[0].serial
	for _, stream := range a.streamUI.views {
		for serial := range stream.unread {
			if serial < first {
				delete(stream.unread, serial)
			}
		}
	}
	for key := range a.folds.expanded {
		if key.serial < first {
			delete(a.folds.expanded, key)
		}
	}
	for id := range a.activityCollapsed {
		if a.outputEntry(id) == nil {
			delete(a.activityCollapsed, id)
		}
	}
	a.renderTranscript(false)
}

func evalDisplayText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const note = "\n… display truncated; full history in trace.jsonl"
	end := limit - len(note)
	for end > 0 && text[end]&0xc0 == 0x80 {
		end--
	}
	return text[:end] + note
}

var _ tea.Model = (*evalModel)(nil)
