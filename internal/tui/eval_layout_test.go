package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func TestEvalBottomQueueNavigationAndGeometry(t *testing.T) {
	m, task := evalSetup(t)
	for i := 2; i <= 40; i++ {
		next := task
		next.ID = fmt.Sprintf("medium-%02d-next-test", i)
		next.Title = fmt.Sprintf("Upcoming test %d", i)
		m.Update(eval.Progress{Task: next, Phase: eval.Queued})
	}
	a := m.current().activity
	a.rememberPlan(planFixture())
	m.resizeActivity(m.current())
	if a.viewport.Width != m.width-2 || strings.Contains(ansi.Strip(m.View()), "Problems  easy") {
		t.Fatal("sidebar still consumes activity space")
	}
	closed := a.viewport.Height
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !m.queueOpen || a.viewport.Height >= closed {
		t.Fatal("queue did not open above the footer")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.current().task.ID != task.ID {
		t.Fatal("browsing queue changed the current problem")
	}
	if !strings.Contains(ansi.Strip(m.View()), "medium-40-next-test") {
		t.Fatal("last queued test is unreachable")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.queueOpen || m.current().task.ID != "medium-40-next-test" || m.followActive {
		t.Fatal("queue selection did not open the chosen test")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if m.current().task.ID != task.ID {
		t.Fatal("follow did not restore active test")
	}
	// Click a visible queue cell using the same measured origin used for drawing.
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, 2, m.queueTop()+1))
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, m.detailWidth()/2+3, m.queueTop()+2))
	if m.current().task.ID != m.problems[1].task.ID {
		t.Fatal("queue mouse target is misplaced")
	}
	for _, size := range []tea.WindowSizeMsg{{Width: 140, Height: 45}, {Width: 80, Height: 30}, {Width: 40, Height: 18}, {Width: 20, Height: 8}, {Width: 1, Height: 1}} {
		m.Update(size)
		for range 2 {
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			view := m.View()
			if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
				t.Fatalf("layout overflow at %+v", size)
			}
		}
	}
	if m.ctx.Err() != nil || a.input.Value() != "" {
		t.Fatal("queue navigation affected execution")
	}
}

func TestProgressReportDisclosureKeepsProvenance(t *testing.T) {
	m, task := evalSetup(t)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 45})
	report := work.Event{Kind: work.WorkProgressReported, Work: work.Work{Assignee: "worker"}, Change: &work.Change{ProgressReports: []work.WorkProgressReport{{ID: "report-source", WorkID: "work-source", Position: &work.WorkPosition{Objective: "Respect the README contract.", Activity: "**Grouping is implemented.**", Note: "- Preserve duplicates\n- Keep output stable"}}}}}
	m.Update(eval.Progress{Task: task, Event: conversation.WorkEvent{Event: report}})
	a := m.current().activity
	before := ansi.Strip(m.View())
	if !strings.Contains(before, "Grouping is implemented.") || !strings.Contains(before, "• Preserve duplicates") || strings.Contains(before, "report-source") {
		t.Fatal("report is not a clean narrative", before)
	}
	target := a.folds.targets[len(a.folds.targets)-1]
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, 1+target.column, m.activityTop()+target.row-a.viewport.YOffset))
	if !strings.Contains(ansi.Strip(m.View()), "report-source") || !strings.Contains(ansi.Strip(m.View()), "Respect the README contract.") {
		t.Fatal("report details are inaccessible", ansi.Strip(m.View()))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF7})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(ansi.Strip(m.View()), "report-source") {
		t.Fatal("keyboard could not collapse report")
	}
	beforeCount := len(a.entries)
	notice := &message.WorkProgressNotice{Reports: []message.ProgressReportRef{{ReportID: "report-source", WorkID: "work-source"}}}
	a.observe(conversation.MessageEvent{Message: message.Message{From: "worker", To: "root", Kind: message.Notification, Progress: notice}})
	if len(a.entries) != beforeCount {
		t.Fatal("duplicate routing notice exposed report IDs outside the disclosure")
	}
	notice.Reports[0].ReportID = "unseen-report"
	a.observe(conversation.MessageEvent{Message: message.Message{From: "worker", To: "root", Kind: message.Notification, Progress: notice}})
	if len(a.entries) != beforeCount+1 || !strings.Contains(a.entries[len(a.entries)-1].body, "unseen-report") {
		t.Fatal("notice for an unavailable report was lost")
	}
}

func TestFocusedPlanWrapsAndCanReadLongStep(t *testing.T) {
	m, _ := focusedSetup(t)
	p := planFixture()
	p.Steps[2].Title = strings.Repeat("Check ordering and duplicate handling. ", 9) + "Title ending."
	p.Steps[2].Note = strings.Repeat("A detailed update.\n\n", 8) + "Final update."
	emitPlan(m, p)
	m.Update(tea.KeyMsg{Type: tea.KeyF8})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := dockText(m)
	if !strings.Contains(view, "Check ordering") || strings.Contains(view, "Define the note model") {
		t.Fatal("plan is not focused", view)
	}
	foundTitle, foundNote := false, false
	for i := 0; i < 30; i++ {
		view = dockText(m)
		foundTitle = foundTitle || strings.Contains(view, "Title ending.")
		foundNote = foundNote || strings.Contains(view, "Final update.")
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !foundTitle || !foundNote {
		t.Fatal("long plan text is inaccessible")
	}
	for i := 0; i < 30; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if !strings.Contains(dockText(m), "Check ordering") {
		t.Fatal("cannot return to beginning of long step")
	}
	// Automatic progress must start the next step at its title, even if the
	// previous step was being read several pages down.
	m.currentPlan().scroll = 12
	p.Steps[2].Status = work.Completed
	emitPlan(m, p)
	if !strings.Contains(dockText(m), "Review persistence and undo") || m.currentPlan().detail != "" {
		t.Fatal("automatic progress retained the previous step's reading position")
	}
}

func TestEvalMockRenderCapture(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprint(dark), func(t *testing.T) {
			withTerminalTheme(t, dark, termenv.TrueColor)
			m, task := evalSetup(t)
			m.opts.Config.Model.Model = "qwen3.6"
			m.opts.Parallel = 1
			m.current().task.Title = "Group tags that are rearrangements of each other"
			m.current().task.ID = "medium-02-group-equivalent-tags"
			fixed := m.started.Add(202 * time.Second)
			m.now = func() time.Time { return fixed }
			m.current().started = fixed.Add(-104 * time.Second)
			m.current().last = fixed
			for i, name := range []string{"merge-busy-periods", "top-endpoints", "longest-distinct-streak", "exclusive-products", "fewest-packages", "longest-improvement"} {
				next := task
				next.ID = fmt.Sprintf("medium-%02d-%s", i+3, name)
				m.Update(eval.Progress{Task: next, Phase: eval.Queued})
			}
			a := m.current().activity
			a.entries = nil
			a.addEntry(entry{label: "Progress", meta: "agent-2", actors: []message.ActorID{"agent-2"}, body: "**Equivalent tags now land in the same group.** Each tag gets a canonical key, so rearranging its letters doesn’t change the result.\n\n- Keep repeated tags in their original multiplicity.\n- Sort each group, then sort the groups for **deterministic output**.\n\nNext, check empty inputs and duplicates, then run `go vet ./...`.", reportDetail: &entry{label: "Progress", body: "Objective: Group equivalent tags.\n\nreport-jexlned · work-3yijugx · revision 2 · assignment 1"}}, false)
			p := planFixture()
			p.Title = "Group equivalent tags"
			p.Steps = p.Steps[:4]
			p.Steps[0].Title = "Confirm the input and output contract"
			p.Steps[1].Title = "Group equivalent tags"
			p.Steps[2].Title = "Check ordering and edge cases"
			p.Steps[2].Note = "Check empty inputs and repeated tags, then confirm every group is returned in a consistent order."
			p.Steps[3].Title = "Review and accept the result"
			p.Steps[3].Status = work.Pending
			a.rememberPlan(p)
			for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 42}, {Width: 80, Height: 30}} {
				m.Update(size)
				view := m.View()
				if lipgloss.Width(view) > size.Width || lipgloss.Height(view) > size.Height {
					t.Fatal("overflow")
				}
				if dir := os.Getenv("STRAP_LAYOUT_CAPTURE"); dir != "" {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%t-%d.ansi", dark, size.Width)), []byte(view), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
