package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// Find an exposed cell, not the covered center of an overlapped chip.
func stackLocation(t *testing.T, m *model, choice rosterChoice) (int, int) {
	t.Helper()
	l := m.stackLayout()
	s := m.stackSurface(l, false)
	for y := 0; y < s.height; y++ {
		for x := 0; x < s.width; x++ {
			i := s.hit(x, y)
			if i >= 0 && l.targets[i].choice == choice {
				return x + 2, y + 2
			}
		}
	}
	t.Fatalf("stack target %+v is not visible:\n%s", choice, ansi.Strip(m.View()))
	return 0, 0
}

func TestAgentStacksKeepTranscriptAndComposerFullWidth(t *testing.T) {
	m, _ := focusedSetup(t)
	if m.viewport.Width != m.width-2 || m.stackBarHeight() != 5 {
		t.Fatal("stacks did not replace the sidebar")
	}
	if m.transcriptTop() != 9 {
		t.Fatal("transcript does not account for stack rows")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "All activity") || !strings.Contains(view, "Idle  2") {
		t.Fatal(view)
	}
	if m.composerTop()+m.input.Height()+3 != m.height {
		t.Fatal("composer no longer ends at the terminal bottom")
	}
}

func TestAgentStacksRootHasLiveChipAndPreview(t *testing.T) {
	m, _ := focusedSetup(t)
	m.selectStream("agent-2")
	progress(m, m.session.Root(), "Coordinating the team")
	m.working[m.session.Root()] = true
	var root stackTarget
	for _, target := range m.stackLayout().targets {
		if target.choice.id == m.session.Root() {
			root = target
		}
	}
	if root.height != 3 || !strings.Contains(ansi.Strip(m.stackTargetView(root, false)), "● root") {
		t.Fatal("root is missing its live agent chip")
	}
	x, y := stackLocation(t, m, rosterChoice{id: m.session.Root()})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	p := m.stackPeek()
	if p == nil || !strings.Contains(ansi.Strip(p.text), "Coordinating the team") {
		t.Fatal("root hover is missing its latest update")
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	if m.streamUI.selected != m.session.Root() || !m.input.Focused() {
		t.Fatal("root chip did not open the root conversation")
	}
}

func TestAgentStacksWrapToExposeEveryAgent(t *testing.T) {
	m := manyAgents(t)
	for _, width := range []int{124, 80} {
		m.resize(width, 38)
		l := m.stackLayout()
		if l.offset != 0 || l.width > width-4 || m.stackBarHeight() <= 5 {
			t.Fatalf("team did not wrap at width %d: %+v", width, l)
		}
		for _, id := range m.streamUI.order {
			stackLocation(t, m, rosterChoice{id: id})
		}
		if m.composerTop()+m.input.Height()+3 != m.height || m.viewport.Height < 4 {
			t.Fatal("wrapped team displaced the conversation or composer")
		}
	}
	// A wrapped row uses the same screen coordinates for hover and clicks.
	x, y := stackLocation(t, m, rosterChoice{id: "agent-11"})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	if m.streamUI.hover.id != "agent-11" {
		t.Fatal("wrapped chip could not be previewed")
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	if m.streamUI.selected != "agent-11" {
		t.Fatal("wrapped chip could not be opened")
	}
}

func TestAgentStackPreviewDoesNotNavigateReadOrSend(t *testing.T) {
	m, s := focusedSetup(t)
	m.input.SetValue("keep this draft")
	progress(m, "agent-2", "Checking the mouse hit targets")
	before := m.viewport.View()
	unread := len(m.ensureStream("agent-2").unread)
	if unread == 0 {
		t.Fatal("fixture has no unread update")
	}
	x, y := stackLocation(t, m, rosterChoice{id: "agent-2"})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	if m.streamUI.selected != "root" || m.viewport.View() != before || !m.input.Focused() {
		t.Fatal("hover navigated or stole composer focus")
	}
	if len(m.ensureStream("agent-2").unread) != unread {
		t.Fatal("hover marked an unvisited stream read")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Checking the mouse hit targets") {
		t.Fatal("hover is missing the real latest update")
	}
	x, y = stackLocation(t, m, rosterChoice{id: "agent-3"})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	if m.streamUI.hover.id != "agent-3" {
		t.Fatal("a lifted chip captured a neighbour's target")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.stackPeek() != nil || m.input.Value() != "keep this draft" {
		t.Fatal("Escape failed to dismiss preview without changing draft")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF6})
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.streamUI.focusID != "agent-2" || m.streamUI.selected != "root" || len(m.ensureStream("agent-2").unread) != unread {
		t.Fatal("keyboard preview committed the stream")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.streamUI.selected != "agent-2" || len(m.ensureStream("agent-2").unread) != 0 || len(s.sent) != 0 || m.input.Value() != "keep this draft" {
		t.Fatal("opening preview did not preserve routing and draft")
	}
}

func TestAgentStackPreviewInterceptsClicksAndClearsOnExit(t *testing.T) {
	m, _ := focusedSetup(t)
	x, y := stackLocation(t, m, rosterChoice{id: "agent-2"})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	p := m.stackPeek()
	if p == nil {
		t.Fatal("no preview")
	}
	for row := y; row < p.y; row++ {
		m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, row))
		if !m.streamUI.hovering {
			t.Fatal("preview disappeared while crossing the gap below the chip")
		}
	}
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, p.x+1, p.y+1))
	if !m.streamUI.hovering {
		t.Fatal("moving into preview dismissed it")
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, p.x+1, p.y+1))
	if m.streamUI.selected != "agent-2" || m.mouseSelection != nil || m.stackPeek() != nil {
		t.Fatal("preview click leaked into transcript")
	}
	x, y = stackLocation(t, m, rosterChoice{id: "agent-3"})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, m.width-1, m.height-1))
	if m.stackPeek() != nil {
		t.Fatal("preview stayed open outside stacks")
	}
}

func TestAgentStackOverflowKeepsEveryKeyboardTargetReachable(t *testing.T) {
	m := manyAgents(t)
	m.resize(40, 24)
	m.focusRoster(true)
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	for i, want := range m.rosterChoices() {
		if i > 0 {
			m.Update(tea.KeyMsg{Type: tea.KeyTab})
		}
		if m.rosterCursor() != want {
			t.Fatalf("skipped %+v", want)
		}
		_ = m.View()
		stackLocation(t, m, want)
		if m.streamUI.selected != "root" {
			t.Fatal("paging preview navigated the transcript")
		}
	}
	if m.streamUI.stackOffset == 0 {
		t.Fatal("overflow never scrolled")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	_ = m.View()
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, m.width-2, 4))
	_ = m.View()
	if m.streamUI.stackOffset == 0 {
		t.Fatal("overflow arrow did not page the stacks")
	}
}

func TestAgentStacksFrozenPreviewSurvivesUpdatesAndResize(t *testing.T) {
	m, _ := focusedSetup(t)
	progress(m, "agent-2", "BEFORE UPDATE")
	x, y := stackLocation(t, m, rosterChoice{id: "agent-2"})
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonNone, x, y))
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	before := m.View()
	progress(m, "agent-2", "AFTER UPDATE")
	if m.View() != before {
		t.Fatal("frozen preview changed")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if strings.Contains(m.View(), "AFTER UPDATE") {
		t.Fatal("resize leaked live preview data into frozen display")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m.focusRoster(true)
	m.streamUI.focusID = "agent-2"
	if !strings.Contains(m.View(), "AFTER UPDATE") {
		t.Fatal("resuming did not show latest status")
	}
}

func TestAgentStacksRevealNewlyCompletedSelection(t *testing.T) {
	m := manyAgents(t)
	m.toggleCompleted()
	m.selectStream("agent-2")
	w, _ := m.streamWork("agent-2")
	w.State = work.Accepted
	w.Revision++
	m.rememberWork(w)
	_ = m.View()
	if !m.streamUI.completedExpanded {
		t.Fatal("finishing work hid the selected chip")
	}
	stackLocation(t, m, rosterChoice{id: message.ActorID("agent-2")})
}

func TestAgentStackShortPreviewKeepsLatestUpdateAboveComposer(t *testing.T) {
	m, _ := focusedSetup(t)
	m.resize(40, 18)
	progress(m, "agent-2", "LATEST UPDATE")
	m.focusRoster(true)
	m.streamUI.focusID = "agent-2"
	p := m.stackPeek()
	if p == nil || !strings.Contains(ansi.Strip(p.text), "LATEST UPDATE") {
		t.Fatal("short preview hid the latest update")
	}
	if p.y+len(strings.Split(p.text, "\n")) > m.composerTop() {
		t.Fatal("preview covers composer")
	}
}
