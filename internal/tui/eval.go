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
	reused        bool
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
	ctx           context.Context
	cancel        context.CancelFunc
	opts          eval.Options
	problems      []*evalProblem
	byID          map[string]*evalProblem
	selected      int
	width, height int
	listOffset    int
	spinner       spinner.Model
	started       time.Time
	now           func() time.Time
	stopping      bool
	followActive  bool
	lastLog       string
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
		p.result, p.reused = &copy, e.Reused
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
		if copy.Reply != "" && e.Reused {
			p.activity.add("Saved reply", safeText(copy.Reply), false)
		}
		if copy.TimedOut {
			p.activity.add("Eval", "Session budget exhausted; workspace was still graded.", false)
		}
		if copy.NoReply {
			p.activity.add("Eval", "Session ended without a root reply; workspace was still graded.", false)
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
		a := p.activity
		if a != nil && key == "esc" && a.badges.peek != nil {
			a.badges.peek = nil
			return m, nil
		}
		if a != nil {
			a.badges.peek = nil
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
		left := m.listWidth()
		if p.activity != nil {
			x := 1
			if left > 0 {
				x += left + 3
			}
			if p.activity.badgeMouse(v, x, 15, m.width, m.height-2) {
				return m, nil
			}
		}
		if v.Action == tea.MouseActionPress && v.Button == tea.MouseButtonLeft && left > 0 && v.X < left && v.Y >= 7 {
			index := m.listOffset + (v.Y-7)/2
			if index < len(m.problems) && v.Y < m.height-2 {
				m.followActive = false
				m.selected = index
			}
		} else if p.activity != nil && tea.MouseEvent(v).IsWheel() {
			m.followActive = false
			p.activity.viewport, _ = p.activity.viewport.Update(v)
		} else if p.activity != nil && v.Action == tea.MouseActionPress && v.Button == tea.MouseButtonLeft {
			// Header has six rows, and the selected problem has nine rows above
			// its activity viewport. Fold targets use viewport content coordinates.
			const top = 15
			x := v.X - 1
			if left > 0 {
				x -= left + 3
			}
			a := p.activity
			y := v.Y - top + a.viewport.YOffset
			if v.Y >= top && v.Y < top+a.viewport.Height {
				for _, target := range append(append([]foldTarget{}, a.folds.targets...), a.folds.hints...) {
					if y == target.row && x >= target.column && x < target.column+2 {
						m.followActive = false
						a.toggleFold(target.key)
						break
					}
				}
			}
		}
	}
	return m, nil
}

func (m *evalModel) listWidth() int {
	if m.width < 90 {
		return 0
	}
	return 32
}
func (m *evalModel) detailWidth() int { return max(1, m.width-m.listWidth()-4) }
func (m *evalModel) resizeActivity(p *evalProblem) {
	if p.activity != nil {
		p.activity.badges.peek = nil
	}
	if p.activity == nil {
		return
	}
	a := p.activity
	a.width = m.detailWidth()
	a.viewport.Width = a.width
	a.viewport.Height = max(1, m.height-18)
	a.renderTranscript(false)
}

func (m *evalModel) View() string {
	if m.width < 2 {
		return " "
	}
	width := max(1, m.width-2)
	clip := func(s string) string { return ansi.Truncate(s, width, "…") }
	done, passed, failed, active, reused := 0, 0, 0, 0, 0
	for _, p := range m.problems {
		if p.phase == eval.Finished {
			done++
			if p.result != nil && p.result.Passed {
				passed++
			} else {
				failed++
			}
		}
		if p.active() {
			active++
		}
		if p.reused {
			reused++
		}
	}
	state := "RUNNING"
	if m.stopping {
		state = "STOPPING"
	}
	header := titleStyle.Render("strap / eval") + fmt.Sprintf("  %s · %d workers · %s", safeText(m.opts.Config.Model.Model), max(1, m.opts.Parallel), state)
	count := fmt.Sprintf("%d / %d problems complete · elapsed %s", done, len(m.problems), m.now().Sub(m.started).Round(time.Second))
	barWidth := max(1, min(60, width))
	filled := 0
	if len(m.problems) > 0 {
		filled = barWidth * done / len(m.problems)
	}
	passedWidth := 0
	if len(m.problems) > 0 {
		passedWidth = barWidth * passed / len(m.problems)
	}
	bar := successStyle.Render(strings.Repeat("━", passedWidth)) + errorStyle.Render(strings.Repeat("━", filled-passedWidth)) + dimStyle.Render(strings.Repeat("─", barWidth-filled))
	counts := successStyle.Render(fmt.Sprintf("✓ %d passed", passed)) + " · " + errorStyle.Render(fmt.Sprintf("× %d failed", failed)) + fmt.Sprintf(" · %d active · %d queued", active, len(m.problems)-done-active)
	if reused > 0 {
		counts += fmt.Sprintf(" · %d reused", reused)
	}
	var tiers []string
	for _, tier := range eval.Tiers() {
		total, complete := 0, 0
		for _, p := range m.problems {
			if p.task.Tier == tier {
				total++
				if p.phase == eval.Finished {
					complete++
				}
			}
		}
		if total > 0 {
			tiers = append(tiers, fmt.Sprintf("%s %d/%d", tier, complete, total))
		}
	}
	lines := []string{header, "", count, bar, counts, dimStyle.Render(strings.Join(tiers, " · "))}
	bodyHeight := max(1, m.height-8)
	detail := m.detailLines(bodyHeight)
	left := m.listWidth()
	if left > 0 {
		list := m.problemLines(bodyHeight)
		for i := 0; i < bodyHeight; i++ {
			lines = append(lines, fitStreamCell(list[i], left)+dimStyle.Render(" │ ")+detail[i])
		}
	} else {
		lines = append(lines, detail...)
	}
	footer := "↑↓ problems · f follow · ^T output · Tab agents · PgUp/Dn scroll · ^C stop"
	if m.width < 80 {
		footer = "↑↓ problems · ^T output · PgUp/Dn scroll · ^C stop"
	}
	if m.width < 55 {
		footer = "↑↓ select · ^T output · ^C stop"
	}
	if p := m.current(); p != nil && p.activity != nil && p.activity.folds.focused {
		footer = "↑/↓ activity · Enter expand · Esc problems · PgUp/Dn scroll · Ctrl+C stop run"
	}
	statusLine := "READ ONLY · results → " + safeText(m.opts.Output)
	if m.lastLog != "" {
		statusLine = "READ ONLY · " + inlineText(m.lastLog)
	}
	lines = append(lines, dimStyle.Render(footer), dimStyle.Render(statusLine))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		lines[i] = " " + clip(lines[i])
	}
	view := strings.Join(lines, "\n")
	if p := m.current(); p != nil && p.activity != nil {
		return p.activity.overlayBadgePeek(view, m.width, m.height)
	}
	return view
}

func (m *evalModel) problemLines(height int) []string {
	rows := make([]string, height)
	rows[0] = titleStyle.Render("Problems") + dimStyle.Render("  easy → medium → hard")
	capacity := max(1, (height-1)/2)
	if m.selected < m.listOffset {
		m.listOffset = m.selected
	}
	if m.selected >= m.listOffset+capacity {
		m.listOffset = m.selected - capacity + 1
	}
	for i := m.listOffset; i < len(m.problems) && i < m.listOffset+capacity; i++ {
		p := m.problems[i]
		marker := "·"
		if p.active() {
			marker = m.spinner.View()
		}
		if p.result != nil {
			if p.result.Passed {
				marker = successStyle.Render("✓")
			} else {
				marker = errorStyle.Render("×")
			}
		}
		label := marker + " " + safeText(strings.TrimPrefix(p.task.ID, p.task.Tier+"-"))
		if i == m.selected {
			label = titleStyle.Render("› " + strings.TrimSpace(label))
		}
		r := (i-m.listOffset)*2 + 1
		if r < height {
			rows[r] = label
		}
		if r+1 < height {
			rows[r+1] = dimStyle.Render("  " + p.task.Tier + " · " + m.problemStatus(p))
		}
	}
	return rows
}

func (m *evalModel) problemStatus(p *evalProblem) string {
	if p.reused {
		return p.status + " · reused"
	}
	if p.phase == eval.Queued {
		return "queued"
	}
	duration := m.now().Sub(p.started)
	if p.result != nil {
		duration = p.result.Duration
	}
	return p.status + " · " + duration.Round(time.Second).String()
}

func (m *evalModel) detailLines(height int) []string {
	rows := []string{}
	p := m.current()
	if p == nil {
		rows = append(rows, "Loading task ladder…")
	} else {
		rows = append(rows, titleStyle.Render(safeText(p.task.Title)), dimStyle.Render(safeText(p.task.ID)))
		status := m.problemStatus(p)
		if p.active() {
			status = m.spinner.View() + " " + status + " / " + p.task.SessionTimeout().String() + " budget"
		}
		rows = append(rows, status)
		contextLabel := "Context: unavailable"
		stream := "all agents"
		if p.activity != nil {
			id := p.activity.streamUI.selected
			if id != "" {
				stream = string(id)
			}
			// Context is a measurement for one agent, never a sum of call usage.
			if id == "" {
				id = p.root
			}
			if c := p.activity.ensureStream(id).context; c != nil {
				contextLabel = "Context: " + c.label()
				if !c.failed && !c.pending {
					contextLabel += " (last measured)"
				}
			}
			if id != "" {
				contextLabel += " · " + string(id)
			}
		}
		tools := fmt.Sprintf("Tools: %d · %d running · %d errors · %d model calls", len(p.tools), p.runningTools(), len(p.toolErrors), len(p.outputs))
		if p.reused {
			tools = "Live metrics unavailable · result reused"
		}
		rows = append(rows, contextLabel, tools)
		usage := "Tokens in/out: unavailable"
		if p.usageCalls > 0 {
			usage = fmt.Sprintf("Tokens in/out: %s / %s", tokenDigits(p.input), tokenDigits(p.output))
			if p.missingUsage {
				usage += " (partial)"
			}
		}
		rows = append(rows, dimStyle.Render(usage), "")
		follow := "following latest"
		if p.activity != nil && !p.activity.viewport.AtBottom() {
			follow = "scrolled · Ctrl+End to follow"
		}
		if p.phase == eval.Finished {
			follow = "recorded activity"
		}
		if !p.last.IsZero() && p.active() && m.now().Sub(p.last) > 30*time.Second {
			follow = "last event " + m.now().Sub(p.last).Round(time.Second).String() + " ago"
		}
		rows = append(rows, "Activity / "+safeText(stream)+" · "+follow, "")
		if p.activity != nil {
			rows = append(rows, strings.Split(p.activity.viewport.View(), "\n")...)
		} else {
			rows = append(rows, dimStyle.Render("Waiting for an available worker."))
		}
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	rows = rows[:height]
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], m.detailWidth(), "…")
	}
	return rows
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
