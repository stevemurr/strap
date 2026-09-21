package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/work"
)

// A plan is a persistent projection of accepted workflow events, not a chat
// entry. Disclosure and keyboard state never change the workflow itself.
var planWorkingBackground = lipgloss.AdaptiveColor{Light: "#F0F5FF", Dark: "#202938"}

type planView struct {
	plan             work.Plan
	expanded         bool
	scroll           int
	selected, detail work.StepID
}
type planRenderKey struct {
	width, budget           int
	scroll                  int
	generation              uint64
	active                  work.PlanID
	selected, detail        work.StepID
	expanded, focused, dark bool
	profile                 termenv.Profile
}
type planDock struct {
	generation uint64
	cacheKey   planRenderKey
	cache      []planLine
	views      map[work.PlanID]*planView
	order      []work.PlanID
	active     work.PlanID
	focused    bool
	frozen     []planLine
}
type planLine struct {
	text string
	kind string
	step work.StepID
}

func (m *model) rememberPlan(p work.Plan) {
	if p.ID == "" {
		return
	}
	if m.plans.views == nil {
		m.plans.views = map[work.PlanID]*planView{}
	}
	v := m.plans.views[p.ID]
	if v != nil && v.plan.Revision > p.Revision {
		return
	}
	if v == nil {
		v = &planView{expanded: true, scroll: -1}
		m.plans.views[p.ID] = v
		m.plans.order = append(m.plans.order, p.ID)
		current := m.currentPlan()
		if current == nil || (!m.plans.focused && p.Owner == m.session.Root() && (current.plan.Owner != p.Owner || planFinished(current.plan))) {
			m.plans.active = p.ID
		}
	}
	// Progress updates can change step status without changing plan structure's
	// revision. Equal revisions must still be applied in event stream order.
	var previous work.StepID
	if len(v.plan.Steps) > 0 {
		previous = v.plan.Steps[planCursor(v)].ID
	}
	v.plan = p.Clone()
	if v.selected == "" && len(v.plan.Steps) > 0 && previous != v.plan.Steps[planCursor(v)].ID {
		v.scroll, v.detail = -1, ""
	}
	m.plans.generation++
}
func (m *model) observePlanEvent(e work.Event) {
	if e.Plan != nil {
		m.rememberPlan(*e.Plan)
	}
	if e.Change != nil {
		for _, p := range e.Change.Plans {
			m.rememberPlan(p)
		}
	}
	// Older recordings may contain only changed steps and a scoped work item.
	if e.Plan == nil && (e.Change == nil || len(e.Change.Plans) == 0) && e.Work.Scope != nil {
		if previous, ok := m.streamUI.works[e.Work.ID]; ok && previous.Revision > e.Work.Revision {
			return
		}
		if v := m.plans.views[e.Work.Scope.PlanID]; v != nil {
			p := v.plan.Clone()
			for _, step := range e.Steps {
				for i := range p.Steps {
					if p.Steps[i].ID == step.ID {
						p.Steps[i] = step
					}
				}
			}
			m.rememberPlan(p)
		}
	}
}
func (m *model) currentPlan() *planView { return m.plans.views[m.plans.active] }
func planFinished(p work.Plan) bool {
	if len(p.Steps) == 0 {
		return false
	}
	for _, s := range p.Steps {
		if s.Status != work.Completed && s.Status != work.CancelledStep {
			return false
		}
	}
	return true
}
func planStatus(status work.StepStatus) (string, string, lipgloss.Style) {
	switch status {
	case work.Completed:
		return "✓", "Complete", successStyle
	case work.InProgress:
		return "●", "In progress", accentStyle
	case work.ReadyForReview:
		return "◇", "Ready for review", lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#9333EA", Dark: "#C084FC"})
	case work.Blocked:
		return "!", "Blocked", stateStyle
	case work.CancelledStep:
		return "–", "Cancelled", dimStyle
	case work.Pending, "":
		return "○", "Pending", dimStyle
	default:
		return "?", "Unknown", dimStyle
	}
}
func planSummary(p work.Plan) (string, int) {
	return planSummaryTitle(p, inlineText)
}

func planSummaryTitle(p work.Plan, title func(string) string) (string, int) {
	done, cancelled := 0, 0
	for _, s := range p.Steps {
		if s.Status == work.Completed {
			done++
		}
		if s.Status == work.CancelledStep {
			cancelled++
		}
	}
	for _, status := range []work.StepStatus{work.Blocked, work.InProgress, work.ReadyForReview, work.Pending, ""} {
		for _, step := range p.Steps {
			if step.Status == status {
				mark, label, style := planStatus(status)
				return style.Render(mark+" "+label) + " · " + title(step.Title), done
			}
		}
	}
	if len(p.Steps) == 0 {
		return dimStyle.Render("No steps yet"), done
	}
	if done+cancelled < len(p.Steps) {
		return dimStyle.Render("Status unavailable"), done
	}
	if cancelled == len(p.Steps) {
		return dimStyle.Render("Cancelled"), done
	}
	if cancelled > 0 {
		return dimStyle.Render(fmt.Sprintf("Finished · %d cancelled", cancelled)), done
	}
	return successStyle.Render("✓ Complete · all steps accepted"), done
}

// The mock's segmented track: one segment per step, with status glyphs so the
// same information survives NO_COLOR. Large plans use a proportional track.
func planProgress(p work.Plan, width int) string {
	if len(p.Steps) == 0 || width < 3 {
		return ""
	}
	if len(p.Steps)*3-1 <= width {
		segments := make([]string, len(p.Steps))
		for i, step := range p.Steps {
			mark, _, style := planStatus(step.Status)
			switch step.Status {
			case work.Completed:
				segments[i] = style.Render("━━")
				continue
			case work.Pending, "":
				mark = "─"
			}
			segments[i] = style.Render(mark + "─")
		}
		return strings.Join(segments, " ")
	}
	_, done := planSummary(p)
	cells := min(20, width)
	filled := done * cells / len(p.Steps)
	return successStyle.Render(strings.Repeat("━", filled)) + dimStyle.Render(strings.Repeat("─", cells-filled))
}

func planMarkdown(renderer *glamour.TermRenderer, source string) string {
	text := safeText(source)
	if renderer != nil {
		if rendered, err := renderer.Render(text); err == nil {
			lines := strings.Split(markdownText(rendered), "\n")
			for i, line := range lines {
				// Glamour pads with styled spaces; ordinary TrimSpace cannot
				// remove those and would make short titles appear truncated.
				visible := strings.TrimRight(ansi.Strip(line), " \t")
				lines[i] = ansi.Cut(line, 0, ansi.StringWidth(visible))
			}
			return strings.Trim(strings.Join(lines, "\n"), "\n")
		}
	}
	return text
}
func (m *model) planStepWork(p work.Plan, step work.StepID) (work.Work, bool) {
	var fallback work.Work
	for i := len(m.streamUI.workOrder) - 1; i >= 0; i-- {
		w := m.streamUI.works[m.streamUI.workOrder[i]]
		if w.Scope == nil || w.Scope.PlanID != p.ID || !slices.Contains(w.Scope.StepIDs, step) || w.Assignee == "" {
			continue
		}
		if !workFinished(w) {
			return w, true
		}
		if fallback.ID == "" {
			fallback = w
		}
	}
	return fallback, fallback.ID != ""
}
func planCursor(v *planView) int {
	for i, s := range v.plan.Steps {
		if v.selected != "" && s.ID == v.selected {
			return i
		}
	}
	for _, status := range []work.StepStatus{work.Blocked, work.InProgress, work.ReadyForReview, work.Pending} {
		for i, s := range v.plan.Steps {
			if s.Status == status {
				return i
			}
		}
	}
	return 0
}
func (m *model) planBudget() int {
	if m.height < 10 || m.width < 16 {
		return 0
	}
	// Input and suggestions retain priority. Leave room to read the conversation.
	return max(0, min(14, m.height-4-m.input.Height()-m.completionHeight()-m.streamChrome()-m.stackBarHeight()-max(2, min(6, m.height/3))))
}
func (m *model) planHeight() int { return len(m.planLines(m.viewport.Width, m.planBudget())) }
func (m *model) planTop() int {
	return m.transcriptTop() + m.viewport.Height + m.streamChrome() + m.completionHeight()
}

// Shared by the conversation and eval host. The host supplies its own budget;
// a long plan scrolls within that budget instead of displacing the composer.
func (m *model) planLines(width, budget int) []planLine {
	if budget <= 0 || width < 1 {
		return nil
	}
	if m.selecting {
		return m.plans.frozen[:min(budget, len(m.plans.frozen))]
	}
	v := m.currentPlan()
	if v == nil {
		return nil
	}
	key := planRenderKey{scroll: v.scroll, width: width, budget: budget, generation: m.plans.generation, active: m.plans.active, selected: v.selected, detail: v.detail, expanded: v.expanded, focused: m.plans.focused, dark: lipgloss.HasDarkBackground(), profile: lipgloss.ColorProfile()}
	if m.plans.cache != nil && m.plans.cacheKey == key {
		return m.plans.cache
	}
	p := v.plan
	contentWidth := max(1, min(76, width-4))
	renderer, _ := newMarkdownRenderer(contentWidth, key.dark, key.profile)
	inline := func(source string) string {
		var lines []string
		for _, line := range strings.Split(planMarkdown(renderer, source), "\n") {
			visible := ansi.Strip(line)
			if strings.TrimSpace(visible) != "" {
				left := ansi.StringWidth(visible) - ansi.StringWidth(strings.TrimLeft(visible, " \t"))
				lines = append(lines, ansi.Cut(line, left, ansi.StringWidth(visible)))
			}
		}
		return strings.Join(lines, " ")
	}
	summary, done := planSummaryTitle(p, inline)
	arrow := "▸"
	if v.expanded && budget >= 4 {
		arrow = "▾"
	}
	prefix := arrow + " Plan"
	if len(m.plans.order) > 1 {
		prefix += fmt.Sprintf(" %d/%d", slices.Index(m.plans.order, p.ID)+1, len(m.plans.order))
	}
	count := fmt.Sprintf("%d/%d complete", done, len(p.Steps))
	if width < 45 {
		count = fmt.Sprintf("%d/%d", done, len(p.Steps))
	}
	progress := planProgress(p, min(23, max(0, width/4)))
	if !v.expanded && width < 70 {
		progress = ""
	}
	right := dimStyle.Render(count)
	if progress != "" {
		right = progress + "  " + right
	}
	left := titleStyle.Render(prefix)
	if !v.expanded || budget < 4 || len(p.Steps) == 0 {
		if len(p.Steps) > 0 {
			// Browsing another row does not change the current work item.
			step := p.Steps[planCursor(&planView{plan: p})]
			switch step.Status {
			case work.Blocked, work.InProgress, work.ReadyForReview, work.Pending, "":
				mark, _, style := planStatus(step.Status)
				summary = style.Render(mark) + " " + inline(step.Title)
			}
		}
		left += " · " + summary
	} else if !m.embedded && width >= 70 {
		left += " · " + inline(p.Title)
	}
	room := width - ansi.StringWidth(right) - 2
	header := fitStreamCell(left, max(1, room)) + "  " + right
	rows := []planLine{{text: dimStyle.Render(strings.Repeat("─", width))}, {text: header, kind: "header"}}
	if budget == 1 {
		rows = rows[1:]
	}
	if v.expanded && budget >= 4 && len(p.Steps) > 0 {
		cursor := planCursor(v)
		var body []planLine
		selectedStart := 0
		for i, step := range p.Steps {
			if v.detail != "" && i != cursor {
				continue
			}
			start := len(body)
			mark, label, style := planStatus(step.Status)
			owner := p.Owner
			assigned, ok := m.planStepWork(p, step.ID)
			if ok {
				owner = assigned.Assignee
				if step.Status == work.ReadyForReview && assigned.Kind == work.AuditWork && !workFinished(assigned) {
					label = "In review"
				}
			}
			title := inline(step.Title)
			if step.Status == work.InProgress || i == cursor && m.plans.focused {
				title = lipgloss.NewStyle().Bold(true).Render(title)
			}
			meta := style.Render(label)
			ownerText := agentIcon(owner) + " " + dimStyle.Render(inlineText(string(owner)))
			if v.detail != "" {
				for j, line := range strings.Split(ansi.Hardwrap(ansi.Wrap(title, contentWidth, ""), contentWidth, true), "\n") {
					prefix := "  "
					if j == 0 {
						prefix = style.Render(mark) + " "
					}
					body = append(body, planLine{text: prefix + line, kind: "step", step: step.ID})
				}
				if owner != "" {
					meta += " · " + ownerText
				}
				body = append(body, planLine{text: "  " + meta, kind: "step", step: step.ID})
				detail := step.Note
				if detail == "" && ok {
					detail = assigned.Note
					if assigned.Blocker != "" {
						detail = "Blocked: " + assigned.Blocker
					}
				}
				if len(step.AcceptanceCriteria) > 0 {
					detail += "\n\n**Acceptance criteria**\n\n"
					for _, criterion := range step.AcceptanceCriteria {
						detail += "- " + criterion + "\n"
					}
				}
				if detail == "" {
					detail = "No update yet."
				}
				for _, line := range strings.Split(ansi.Hardwrap(planMarkdown(renderer, detail), contentWidth, true), "\n") {
					body = append(body, planLine{text: "  " + line, kind: "step", step: step.ID})
				}
			} else {
				// The outline is flat: completed, current and pending steps each
				// get one row. Longer text remains available through Enter.
				right := ""
				if width >= 45 {
					right = "  " + fitStreamCell(meta, 16)
				}
				if width >= 80 && owner != "" {
					right += " " + fitStreamCell(ownerText, 15)
				}
				titleWidth := max(1, width-2-ansi.StringWidth(right))
				body = append(body, planLine{text: style.Render(mark) + " " + fitStreamCell(title, titleWidth) + right, kind: "step", step: step.ID})
			}
			if i == cursor {
				selectedStart = start
			}
			if step.Status == work.InProgress || i == cursor && m.plans.focused {
				for j := start; j < len(body); j++ {
					body[j].text = renderSurface(lipgloss.NewStyle().Foreground(surfaceTextColor).Background(planWorkingBackground), fitStreamCell(body[j].text, width))
				}
			}
		}
		capacity := max(1, budget-len(rows)-1)
		start := min(max(0, v.scroll), max(0, len(body)-capacity))
		if v.scroll < 0 {
			start = max(0, selectedStart-capacity+1)
		}
		v.scroll, key.scroll = start, start
		end := min(len(body), start+capacity)
		rows = append(rows, body[start:end]...)
		hint := "Ctrl+P fold · F8 steps"
		if m.plans.focused {
			hint = "↑/↓ steps · Enter details · c outline · Esc back"
		}
		if start > 0 || end < len(body) {
			hint = "PgUp/Dn more · " + hint
		}
		kind := ""
		if len(m.plans.order) > 1 {
			hint = "Next plan › · [/] switch · " + hint
			kind = "next"
		}
		rows = append(rows, planLine{text: dimStyle.Render(hint), kind: kind})
	}

	for i := range rows {
		rows[i].text = ansi.Truncate(rows[i].text, width, "…")
	}
	m.plans.cacheKey, m.plans.cache = key, rows
	return rows
}
func planText(rows []planLine) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.text
	}
	return out
}

func (m *model) focusPlan(focus bool) {
	if focus {
		m.focusRoster(false)
		m.folds.focused = false
		m.badges.peek = nil
		m.input.Blur()
		if v := m.currentPlan(); v != nil {
			v.expanded = true
		}
	} else {
		m.input.Focus()
	}
	m.plans.focused = focus
}
func (m *model) movePlanStep(delta int) {
	v := m.currentPlan()
	if v == nil || len(v.plan.Steps) == 0 {
		return
	}
	v.selected = v.plan.Steps[max(0, min(len(v.plan.Steps)-1, planCursor(v)+delta))].ID
	v.scroll, v.detail = -1, ""
}
func (m *model) nextPlan(delta int) {
	if len(m.plans.order) == 0 {
		return
	}
	i := slices.Index(m.plans.order, m.plans.active)
	m.plans.active = m.plans.order[(i+delta+len(m.plans.order))%len(m.plans.order)]
}
func (m *model) togglePlan() {
	if v := m.currentPlan(); v != nil {
		v.expanded = !v.expanded
		v.detail, v.scroll = "", -1
		if !v.expanded && m.plans.focused {
			m.focusPlan(false)
		}
	}
}
func (m *model) planKey(key string) bool {
	if m.currentPlan() == nil {
		return false
	}
	switch key {
	case "ctrl+p":
		m.togglePlan()
		return true
	case "f8":
		m.focusPlan(!m.plans.focused)
		return true
	}
	if !m.plans.focused {
		return false
	}
	switch key {
	case "esc":
		m.focusPlan(false)
	case "up":
		m.movePlanStep(-1)
	case "down":
		m.movePlanStep(1)
	case "home":
		m.movePlanStep(-len(m.currentPlan().plan.Steps))
	case "end":
		m.movePlanStep(len(m.currentPlan().plan.Steps))
	case "[", "left":
		m.nextPlan(-1)
	case "]", "right":
		m.nextPlan(1)
	case "pgup":
		m.currentPlan().scroll = max(0, m.currentPlan().scroll-4)
	case "pgdown":
		m.currentPlan().scroll += 4
	case "c":
		v := m.currentPlan()
		v.detail, v.scroll = "", -1
	case "enter", " ":
		v := m.currentPlan()
		if len(v.plan.Steps) > 0 {
			v.scroll = -1
			id := v.plan.Steps[planCursor(v)].ID
			if v.detail == id {
				v.detail = ""
			} else {
				v.detail = id
			}
		}
	case "ctrl+c", "ctrl+d", "ctrl+t", "f2", "f6", "f7", "ctrl+home", "ctrl+end":
		return false
	}
	return true
}
func (m *model) planMouse(event tea.MouseMsg, left, top, width, budget int) bool {
	if m.mouseSelection != nil && m.mouseSelection.dragging {
		return false
	}
	rows := m.planLines(width, budget)
	if event.X < left || event.X >= left+width || event.Y < top || event.Y >= top+len(rows) {
		return false
	}
	if tea.MouseEvent(event).IsWheel() {
		if v := m.currentPlan(); v != nil {
			v.expanded = true
		}
		delta := 1
		if event.Button == tea.MouseButtonWheelUp || event.Button == tea.MouseButtonWheelLeft {
			delta = -1
		}
		m.movePlanStep(delta)
		return true
	}
	if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
		m.mouseSelection = nil
		r := rows[event.Y-top]
		switch r.kind {
		case "header":
			m.togglePlan()
		case "next":
			m.nextPlan(1)
		case "step":
			if v := m.currentPlan(); v != nil {
				v.selected = r.step
				v.scroll = -1
				if v.detail == r.step {
					v.detail = ""
				} else {
					v.detail = r.step
				}
			}
		}
	}
	return true
}
func (m *model) planCommand(fields []string) {
	if len(fields) > 2 {
		m.add("Error", "Usage: /plan [plan-id]", true)
		return
	}
	if len(fields) == 2 {
		id := work.PlanID(fields[1])
		if m.plans.views[id] == nil {
			m.add("Error", "Unknown plan: "+fields[1], true)
			return
		}
		m.plans.active = id
	}
	if m.currentPlan() == nil {
		m.add("System", "No plan yet.", true)
		return
	}
	m.focusPlan(true)
}
