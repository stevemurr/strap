package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/work"
)

func planFixture() work.Plan {
	return work.Plan{ID: "plan-1", Owner: "root", Revision: 1, Title: "Build a native notes app", Steps: []work.Step{
		{ID: "model", Title: "Define the note model", Status: work.Completed},
		{ID: "editor", Title: "Build the note list and editor", Status: work.Completed},
		{ID: "search", Title: "Add search and keyboard shortcuts", Status: work.InProgress, Note: "Adding quick capture and testing keyboard navigation."},
		{ID: "review", Title: "Review persistence and undo", Status: work.ReadyForReview},
		{ID: "verify", Title: "Run end-to-end checks", Status: work.Pending},
	}}
}
func emitPlan(m *model, p work.Plan) {
	m.Update(received{event: conversation.WorkEvent{Event: work.Event{Kind: work.PlanChanged, Plan: &p}}})
}
func dockText(m *model) string {
	return ansi.Strip(strings.Join(planText(m.planLines(m.viewport.Width, m.planBudget())), "\n"))
}

func TestPlanDockPersistsAcrossHistoryAndTracksAcceptedUpdates(t *testing.T) {
	m, _ := focusedSetup(t)
	p := planFixture()
	emitPlan(m, p)
	for i := 0; i < 50; i++ {
		m.add("Strap", fmt.Sprintf("Conversation message %d", i), true)
	}
	m.input.SetValue("keep this draft")
	before, top := dockText(m), m.planTop()
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	anchor := m.streamPosition().anchor
	if m.viewport.AtBottom() || dockText(m) != before || m.planTop() != top {
		t.Fatal("scrolling displaced the persistent plan")
	}
	// Structural plan revision is unchanged when accepted progress updates steps.
	p.Steps[2].Status = work.Blocked
	p.Steps[2].Note = "Need a decision on archived notes."
	m.Update(received{event: conversation.WorkEvent{Event: work.Event{Kind: work.WorkProgressReported, Change: &work.Change{Plans: []work.Plan{p}}}}})
	if !strings.Contains(dockText(m), "Blocked") || !strings.Contains(dockText(m), "2/5 complete") {
		t.Fatal(dockText(m))
	}
	if m.streamPosition().anchor != anchor || m.input.Value() != "keep this draft" {
		t.Fatal("progress disturbed history or draft")
	}
	// Domain snapshots are detached from the incoming event.
	p.Steps[2].Status = work.Completed
	if m.currentPlan().plan.Steps[2].Status != work.Blocked {
		t.Fatal("event mutation changed cached plan")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.currentPlan().expanded || m.streamPosition().anchor != anchor {
		t.Fatal("collapse moved history")
	}
	for i := range p.Steps {
		p.Steps[i].Status = work.Completed
	}
	emitPlan(m, p)
	if !strings.Contains(dockText(m), "5/5 complete") || !strings.Contains(dockText(m), "all steps accepted") || m.currentPlan().expanded {
		t.Fatal("completion disappeared or changed disclosure", dockText(m))
	}
	enter(m, "/clear")
	if !strings.Contains(dockText(m), "Build a native notes app") {
		t.Fatal("clearing chat deleted the plan")
	}
}

func TestPlanDockInputMouseAndFocus(t *testing.T) {
	m, s := focusedSetup(t)
	emitPlan(m, planFixture())
	m.input.SetValue("draft")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if m.input.Value() != "draftp" {
		t.Fatal("plan shortcut swallowed normal typing")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.plans.focused || !strings.Contains(dockText(m), "Adding quick capture") {
		t.Fatal("keyboard could not inspect step", dockText(m))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.plans.focused || !m.input.Focused() || len(s.sent) != 0 || m.interrupting {
		t.Fatal("Escape did not return to composer")
	}
	x, y := screenLocation(t, m.View(), "Run end-to-end")
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	if m.currentPlan().detail != "verify" || m.mouseSelection != nil {
		t.Fatal("plan click leaked into text selection")
	}
	// Scrolling the dock browses its steps, not the transcript behind it.
	offset := m.viewport.YOffset
	m.Update(tea.MouseMsg{X: 2, Y: m.planTop() + 1, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.currentPlan().selected != "review" || m.viewport.YOffset != offset {
		t.Fatal("plan wheel scrolled chat")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m.Update(tea.KeyMsg{Type: tea.KeyF6})
	if m.plans.focused || !m.streamUI.rosterFocused {
		t.Fatal("plan and roster focus overlapped")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m.Update(tea.KeyMsg{Type: tea.KeyF7})
	if m.plans.focused || !m.folds.focused {
		t.Fatal("plan and tool focus overlapped")
	}
}

func TestPlanDockProjectionAndMultiplePlans(t *testing.T) {
	m, _ := focusedSetup(t)
	p := planFixture()
	p.Revision = 3
	emitPlan(m, p)
	old := p.Clone()
	old.Revision = 2
	old.Title = "Old title"
	emitPlan(m, old)
	if m.currentPlan().plan.Title != p.Title {
		t.Fatal("older structural revision replaced plan")
	}
	w := work.Work{ID: "impl", Revision: 2, Assignee: "agent-2", State: work.Active, Scope: &work.Scope{PlanID: p.ID, StepIDs: []work.StepID{"search"}}}
	m.Update(received{event: conversation.WorkEvent{Event: work.Event{Work: w, Steps: []work.Step{{ID: "search", Title: p.Steps[2].Title, Status: work.Blocked, Note: "Legacy update"}}}}})
	if m.currentPlan().plan.Steps[2].Status != work.Blocked || !strings.Contains(dockText(m), agentGlyph("agent-2")) {
		t.Fatal("legacy event or assignee was lost", dockText(m))
	}
	w.Revision = 1
	m.Update(received{event: conversation.WorkEvent{Event: work.Event{Work: w, Steps: []work.Step{{ID: "search", Status: work.Pending}}}}})
	if m.currentPlan().plan.Steps[2].Status != work.Blocked {
		t.Fatal("stale work reverted step")
	}
	audit := work.Work{ID: "audit", Kind: work.AuditWork, Assignee: "agent-3", State: work.Active, Scope: &work.Scope{PlanID: p.ID, StepIDs: []work.StepID{"review"}}}
	m.Update(received{event: conversation.WorkEvent{Event: work.Event{Work: audit}}})
	if !strings.Contains(dockText(m), "In review") || !strings.Contains(dockText(m), "2/5") {
		t.Fatal("review treated as acceptance")
	}
	second := planFixture()
	second.ID = "plan-2"
	second.Title = "Second plan"
	emitPlan(m, second)
	if m.plans.active != p.ID {
		t.Fatal("new plan stole the current selection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.plans.active != second.ID {
		t.Fatal("second plan is inaccessible")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	enter(m, "/plan plan-1")
	if m.plans.active != p.ID || !m.plans.focused {
		t.Fatal("plan command did not select plan")
	}
}

func TestPlanDockFreezesWithDisplayWhileStateContinues(t *testing.T) {
	m, _ := focusedSetup(t)
	p := planFixture()
	emitPlan(m, p)
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	before := m.View()
	p.Steps[2].Status = work.Completed
	emitPlan(m, p)
	if m.View() != before {
		t.Fatal("new plan state changed frozen display")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(dockText(m), "2/5 complete") {
		t.Fatal("resize exposed live plan during copy")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(dockText(m), "3/5 complete") {
		t.Fatal("resuming failed to show latest plan")
	}
}

func TestPlanDockFitsLongPlansAndTerminalThemes(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%t", dark), func(t *testing.T) {
			withTerminalTheme(t, dark, termenv.TrueColor)
			m, _ := focusedSetup(t)
			p := planFixture()
			for i := 5; i < 30; i++ {
				p.Steps = append(p.Steps, work.Step{ID: work.StepID(fmt.Sprint(i)), Title: strings.Repeat("Long 界 title ", 8), Status: work.Pending, Note: strings.Repeat("A detailed progress update. ", 10)})
			}
			emitPlan(m, p)
			m.input.SetValue("a draft\nwith multiple lines")
			m.Update(tea.KeyMsg{Type: tea.KeyF8})
			m.Update(tea.KeyMsg{Type: tea.KeyEnd})
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			for _, size := range []tea.WindowSizeMsg{{Width: 104, Height: 40}, {Width: 80, Height: 24}, {Width: 40, Height: 18}, {Width: 20, Height: 12}, {Width: 10, Height: 8}, {Width: 1, Height: 1}} {
				m.Update(size)
				view := m.View()
				if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
					t.Fatalf("overflow at %+v", size)
				}
				if size.Height >= 18 && m.composerTop()+m.input.Height()+3 != m.height {
					t.Fatal("plan moved input outside terminal")
				}
				if size.Width >= 40 && !strings.Contains(dockText(m), "2/30") {
					t.Fatal("plan summary vanished", dockText(m))
				}
			}
			m.resize(104, 40)
			m.input.SetValue("")
			m.syncCompletion()
			m.rememberPlan(planFixture())
			m.currentPlan().selected = "search"
			m.currentPlan().detail = "search"
			m.syncCompletion()
			if dir := os.Getenv("STRAP_PLAN_CAPTURE"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%t-plan.ansi", dark)), []byte(m.View()), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestEvalPlanSharesDockAndKeepsProblemsIndependent(t *testing.T) {
	e, task := evalSetup(t)
	p := planFixture()
	e.Update(eval.Progress{Task: task, Event: conversation.WorkEvent{Event: work.Event{Plan: &p}}})
	a := e.current().activity
	if !strings.Contains(ansi.Strip(e.View()), p.Title) || !strings.Contains(ansi.Strip(e.View()), "2/5 complete") {
		t.Fatal("eval omitted plan", ansi.Strip(e.View()))
	}
	expandedHeight := a.viewport.Height
	e.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if a.currentPlan().expanded || a.viewport.Height <= expandedHeight {
		t.Fatal("eval did not reclaim collapsed space")
	}
	e.Update(tea.KeyMsg{Type: tea.KeyF8})
	e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(e.View()), "Adding quick capture") {
		t.Fatal("eval cannot inspect steps")
	}
	e.Update(tea.KeyMsg{Type: tea.KeyEsc})
	rows := a.planLines(e.detailWidth(), e.evalPlanBudget())
	for i, r := range rows {
		if r.kind == "header" {
			e.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, 1+e.listWidth()+3, 15+a.viewport.Height+i))
			break
		}
	}
	if a.currentPlan().expanded {
		t.Fatal("eval dock mouse coordinates are wrong")
	}
	other := task
	other.ID = "another-problem"
	e.Update(eval.Progress{Task: other, Root: "other-root", Phase: eval.Running})
	e.Update(tea.KeyMsg{Type: tea.KeyDown})
	if e.current().activity.currentPlan() != nil {
		t.Fatal("plan leaked between eval problems")
	}
	e.Update(tea.KeyMsg{Type: tea.KeyUp})
	if e.current().activity.currentPlan().expanded {
		t.Fatal("problem switch lost disclosure state")
	}
}

func TestPlanStatusNeverTreatsCancelledOrUnknownStepsAsAccepted(t *testing.T) {
	for _, status := range []work.StepStatus{work.ReadyForReview, work.CancelledStep, "unexpected"} {
		p := work.Plan{Steps: []work.Step{{Status: work.Completed}, {Status: status}}}
		summary, done := planSummary(p)
		if done != 1 || strings.Contains(ansi.Strip(summary), "all steps accepted") {
			t.Fatalf("%s incorrectly treated as accepted: %s", status, summary)
		}
	}
}

func BenchmarkPlanDockCachedRender(b *testing.B) {
	e := renderBenchEval(b)
	p := planFixture()
	for i := 5; i < 500; i++ {
		p.Steps = append(p.Steps, work.Step{ID: work.StepID(fmt.Sprint(i)), Title: "A step in a large plan", Status: work.Pending})
	}
	e.current().activity.rememberPlan(p)
	e.resizeActivity(e.current())
	_ = e.View()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = e.View()
	}
}

func TestPlanDockCacheTracksThemeAndWorkAssignments(t *testing.T) {
	withTerminalTheme(t, false, termenv.TrueColor)
	m, _ := focusedSetup(t)
	p := planFixture()
	emitPlan(m, p)
	light := strings.Join(planText(m.planLines(m.viewport.Width, m.planBudget())), "\n")
	lipgloss.SetHasDarkBackground(true)
	dark := strings.Join(planText(m.planLines(m.viewport.Width, m.planBudget())), "\n")
	if light == dark || ansi.Strip(light) != ansi.Strip(dark) {
		t.Fatal("theme cache changed content or retained light colors")
	}
	lipgloss.SetColorProfile(termenv.Ascii)
	plain := strings.Join(planText(m.planLines(m.viewport.Width, m.planBudget())), "\n")
	if strings.Contains(plain, "\x1b[38;") || strings.Contains(plain, "\x1b[48;") {
		t.Fatal("cached dock retained colors")
	}
	m.rememberWork(work.Work{ID: "assigned", Assignee: "new-worker", State: work.Active, Scope: &work.Scope{PlanID: p.ID, StepIDs: []work.StepID{"search"}}})
	if !strings.Contains(dockText(m), agentGlyph("new-worker")) {
		t.Fatal("assignment did not invalidate cached dock")
	}
}

func TestPlanMarkdownRendersInDockAndDetails(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark=%t", dark), func(t *testing.T) {
			withTerminalTheme(t, dark, termenv.TrueColor)
			m, _ := focusedSetup(t)
			p := planFixture()
			p.Title = "Build a **native notes app**"
			p.Steps[2].Title = "Add **search** and `⌘K` shortcuts"
			p.Steps[2].Note = "Search is **ready**.\n\n- Check *keyboard navigation*\n- Verify `⌘K` opens search"
			emitPlan(m, p)
			m.Update(tea.KeyMsg{Type: tea.KeyF8})
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			rows := m.planLines(104, 12)
			view := ansi.Strip(strings.Join(planText(rows), "\n"))
			for _, want := range []string{"Build a native notes app", "Add search", "shortcuts", "Search is ready.", "• Check keyboard navigation", "• Verify", "2/5 complete"} {
				if !strings.Contains(view, want) {
					t.Fatalf("missing %q:\n%s", want, view)
				}
			}
			if strings.ContainsAny(view, "*`") {
				t.Fatal("literal Markdown leaked into the plan", view)
			}
			for _, row := range rows {
				if strings.Contains(row.text, "\n") || ansi.StringWidth(row.text) > 104 {
					t.Fatal("Markdown escaped its dock row", row)
				}
				if strings.Contains(ansi.Strip(row.text), "\x1b") {
					t.Fatal("focus styling broke Markdown escape sequences", row)
				}
				if row.kind == "step" && row.step == "search" && strings.Contains(ansi.Strip(row.text), "In progress") && !strings.Contains(ansi.Strip(row.text), "Add search") {
					t.Fatal("focused title is unreadable", row)
				}
				if strings.Contains(ansi.Strip(row.text), "native notes app") && strings.Contains(row.text, "…") {
					t.Fatal("styled padding caused a short title to truncate", row)
				}
				if strings.Contains(ansi.Strip(row.text), "Check keyboard") && (row.kind != "step" || row.step != "search") {
					t.Fatal("Markdown detail lost its mouse target", row)
				}
			}
			if dir := os.Getenv("STRAP_PLAN_CAPTURE"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%t-markdown-plan.ansi", dark)), []byte(strings.Join(planText(rows), "\n")), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPlanMarkdownSanitizesControlsAndKeepsBlockStructure(t *testing.T) {
	renderer, err := newMarkdownRenderer(60, false, termenv.TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	input := "## Result\n\n**Ready** &#27;[2J\x1b[2J\n\n```go\nreturn true\n```"
	got := planMarkdown(renderer, input)
	if strings.Contains(ansi.Strip(got), "\x1b") || strings.Contains(got, "\x1b[2J") {
		t.Fatal("plan Markdown retained terminal controls")
	}
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "▎ Result") || !strings.Contains(plain, "return true") || strings.Contains(plain, "```") || !strings.Contains(plain, "\n") {
		t.Fatal("block Markdown was flattened or left unrendered", plain)
	}
}

func TestPlanProgressPersistsCollapsedAndCountsOnlyAcceptedSteps(t *testing.T) {
	withTerminalTheme(t, false, termenv.Ascii)
	m, _ := focusedSetup(t)
	p := planFixture()
	p.Steps = append(p.Steps, work.Step{ID: "cancelled", Status: work.CancelledStep}, work.Step{ID: "blocked", Status: work.Blocked})
	emitPlan(m, p)
	m.togglePlan()
	view := dockText(m)
	if !strings.Contains(view, "━━ ━━ ●─ ◇─ ── –─ !─") || !strings.Contains(view, "2/7 complete") {
		t.Fatal("collapsed dock lost status segments", view)
	}
	for i := 0; i < 30; i++ {
		p.Steps = append(p.Steps, work.Step{Status: work.ReadyForReview})
	}
	bar := ansi.Strip(planProgress(p, 20))
	if ansi.StringWidth(bar) > 20 || strings.Count(bar, "━") != 1 {
		t.Fatal("large-plan track overflows or counts unaccepted work", bar)
	}
	if planProgress(work.Plan{}, 20) != "" {
		t.Fatal("empty plan has misleading progress")
	}
}
