package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func pressed(m *model, k tea.KeyType) { m.Update(tea.KeyMsg{Type: k}) }

// The roster is a keyboard-navigable list: F6 focuses it, the arrows and paging
// keys move the selection, and home and end jump to its ends.
func TestRosterKeyboardNavigation(t *testing.T) {
	m, s := focusedSetup(t)
	if m.streamUI.rosterFocused {
		t.Fatal("the roster starts unfocused so typing goes to the prompt")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF6})
	if !m.streamUI.rosterFocused || !m.input.Focused() == false {
		t.Fatal("F6 did not move focus to the roster")
	}

	pressed(m, tea.KeyEnd)
	last := m.streamUI.order[len(m.streamUI.order)-1]
	if m.streamUI.focusID != last {
		t.Fatal("end did not select the last stream", m.streamUI.focusID)
	}
	pressed(m, tea.KeyHome)
	if m.streamUI.focusID != "" {
		t.Fatal("home did not select the combined transcript", m.streamUI.focusID)
	}
	pressed(m, tea.KeyDown)
	if m.streamUI.focusID != s.Root() {
		t.Fatal("down did not advance to the first agent", m.streamUI.focusID)
	}
	pressed(m, tea.KeyUp)
	if m.streamUI.focusID != "" {
		t.Fatal("up did not move back", m.streamUI.focusID)
	}
	pressed(m, tea.KeyPgDown)
	if m.streamUI.focusID != last {
		t.Fatal("page down did not clamp at the last stream", m.streamUI.focusID)
	}
	pressed(m, tea.KeyPgUp)
	if m.streamUI.focusID != "" {
		t.Fatal("page up did not clamp at the first stream", m.streamUI.focusID)
	}
	// Bracket keys mirror the arrows.
	typeText(m, "]")
	if m.streamUI.focusID != s.Root() {
		t.Fatal("] did not advance the selection", m.streamUI.focusID)
	}
	typeText(m, "[")
	if m.streamUI.focusID != "" {
		t.Fatal("[ did not move the selection back", m.streamUI.focusID)
	}
	// Keys the roster does not own stay available to the rest of the UI.
	m.streamUI.rosterFocused = true
	if m.streamKey(tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Fatal("the roster swallowed an interrupt")
	}
	pressed(m, tea.KeyEsc)
	if m.streamUI.rosterFocused || !m.input.Focused() {
		t.Fatal("esc did not return focus to the prompt")
	}
}

// /focus selects a stream by name, and refuses a name the session has no
// stream for rather than silently selecting nothing.
func TestFocusCommandSelectsStreams(t *testing.T) {
	m, s := focusedSetup(t)
	m.focusCommand([]string{"/focus", "all"})
	if m.streamUI.selected != "" {
		t.Fatal(m.streamUI.selected)
	}
	m.focusCommand([]string{"/focus", "root"})
	if m.streamUI.selected != s.Root() {
		t.Fatal(m.streamUI.selected)
	}
	m.focusCommand([]string{"/focus", "agent-2"})
	if m.streamUI.selected != "agent-2" {
		t.Fatal(m.streamUI.selected)
	}
	m.focusCommand([]string{"/focus"})
	if m.streamUI.selected != s.Root() {
		t.Fatal("a bare /focus did not return to the root", m.streamUI.selected)
	}
	m.focusCommand([]string{"/focus", "nobody"})
	if !strings.Contains(ansi.Strip(m.viewport.View()), "Unknown agent: nobody") {
		t.Fatal("an unknown agent was not reported")
	}
	m.focusCommand([]string{"/focus", "a", "b"})
	if !strings.Contains(ansi.Strip(m.viewport.View()), "Usage: /focus") {
		t.Fatal("a malformed /focus did not print its usage")
	}
}

// Work status is the phrase shown beside an agent in the roster; each state and
// each blocked or in-flight variation reads differently.
func TestWorkStatusPhrasing(t *testing.T) {
	cases := []struct {
		name string
		w    work.Work
		want string
	}{
		{"accepted", work.Work{State: work.Accepted}, "completed"},
		{"closed", work.Work{State: work.Closed}, "closed"},
		{"cancelled", work.Work{State: work.Cancelled}, "cancelled"},
		{"blocked", work.Work{State: work.Active, Blocker: "waiting on review"}, "blocked"},
		{"needs check", work.Work{State: work.NeedsCheck}, "ready for review"},
		{"checking", work.Work{State: work.Checking}, "in review"},
		{"changes requested", work.Work{State: work.ChangesRequested}, "awaiting repair assignment"},
		{"repair assigned", work.Work{State: work.ChangesRequested, ActiveRepairID: "w-2"}, "repair in progress"},
		{"auditing", work.Work{State: work.Active, Kind: work.AuditWork}, "auditing"},
		{"repairing", work.Work{State: work.Active, Kind: work.Repair}, "repairing"},
		{"implementing", work.Work{State: work.Active, Kind: work.Implementation}, "implementing"},
	}
	for _, tc := range cases {
		if got := workStatus(tc.w); got != tc.want {
			t.Fatal(tc.name, got, "!=", tc.want)
		}
	}
	// A finished item is finished regardless of which terminal state it reached.
	for _, state := range []work.State{work.Accepted, work.Closed, work.Cancelled} {
		if !workFinished(work.Work{State: state}) {
			t.Fatal("not treated as finished:", state)
		}
	}
	if workFinished(work.Work{State: work.Active}) {
		t.Fatal("active work was treated as finished")
	}
}

// An agent's role in the roster follows the work it holds.
func TestStreamRoleFollowsAssignedWork(t *testing.T) {
	m, s := focusedSetup(t)
	if got := m.streamRole(s.Root()); got != "root" {
		t.Fatal(got)
	}
	if got := m.streamRole("agent-2"); got != "agent" {
		t.Fatal("an agent with no work was given a role", got)
	}
	m.rememberWork(work.Work{ID: "w-1", Kind: work.AuditWork, State: work.Active, Assignee: "agent-2", Task: "check it"})
	if got := m.streamRole("agent-2"); got != "auditor" {
		t.Fatal(got)
	}
	m.rememberWork(work.Work{ID: "w-1", Kind: work.Implementation, State: work.Active, Assignee: "agent-2", Task: "build it"})
	if got := m.streamRole("agent-2"); got != "implementor" {
		t.Fatal(got)
	}
	m.rememberWork(work.Work{ID: "w-2", Kind: work.Repair, State: work.Active, Assignee: "agent-3", Task: "fix it"})
	if got := m.streamRole("agent-3"); got != "implementor" {
		t.Fatal(got)
	}
}

// A work event carrying a batched change updates every item it names.
func TestWorkChangeUpdatesEveryNamedItem(t *testing.T) {
	m, _ := focusedSetup(t)
	m.observe(conversation.WorkEvent{Event: work.Event{ID: "e-1", Kind: work.WorkAssigned, Change: &work.Change{Works: []work.Work{
		{ID: "w-1", Kind: work.Implementation, State: work.Active, Assignee: "agent-2", Task: "first"},
		{ID: "w-2", Kind: work.AuditWork, State: work.Active, Assignee: "agent-3", Task: "second"},
	}}}})
	if w, ok := m.streamWork("agent-2"); !ok || w.ID != "w-1" {
		t.Fatal("the first item in the change was not recorded", w, ok)
	}
	if w, ok := m.streamWork("agent-3"); !ok || w.ID != "w-2" {
		t.Fatal("the second item in the change was not recorded", w, ok)
	}
}

// The roster renders each agent with its state, role and current work, and
// shows a placeholder rather than an empty panel when nothing is running.
func TestRosterRendersAgentDetail(t *testing.T) {
	m, _ := focusedSetup(t)
	m.observe(conversation.AgentStateChanged{Agent: "agent-2", State: agent.Running, Revision: 2})
	m.rememberWork(work.Work{ID: "w-1", Kind: work.Implementation, State: work.Active, Assignee: "agent-2", Task: "build the thing"})
	m.rememberWork(work.Work{ID: "w-2", Kind: work.Implementation, State: work.Active, Assignee: "agent-3", Task: "blocked thing", Blocker: "waiting on input"})
	m.working["agent-2"] = true
	m.focusRoster(true)
	for _, id := range []message.ActorID{"agent-2", "agent-3"} {
		m.streamUI.focusID = id
		view := ansi.Strip(m.View())
		for _, want := range []string{string(id), m.streamTask(id), m.rosterStatus(id)} {
			if !strings.Contains(view, want) {
				t.Fatalf("preview did not render %q\n%s", want, view)
			}
		}
	}
	if summary := m.streamSummary(); summary == "" {
		t.Fatal("a running agent produced no summary")
	}
	// Errors remain flagged in the roster; selecting the agent shows the detail.
	m.ensureStream("agent-3").err = "provider unreachable"
	if m.rosterStatus("agent-3") != "error" || m.rosterGroup("agent-3") != "Needs attention" {
		t.Fatal("a stream error was not surfaced in the roster")
	}
	m.selectStream("agent-3")
	if !strings.Contains(ansi.Strip(m.View()), "provider unreachable") {
		t.Fatal("a selected stream error was not surfaced")
	}
}

// The roster responds to the mouse wheel by moving the selection.
func TestRosterMouseWheelMovesSelection(t *testing.T) {
	m, _ := focusedSetup(t)
	m.selectStream("")
	if m.stackBarHeight() == 0 {
		t.Fatal("the roster is not visible at this size")
	}
	m.Update(tea.MouseMsg{X: 1, Y: 0, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if !m.streamUI.rosterFocused {
		t.Fatal("scrolling the roster did not focus it")
	}
	advanced := m.streamUI.focusID
	if advanced == "" {
		t.Fatal("the wheel did not advance the selection")
	}
	m.Update(tea.MouseMsg{X: 1, Y: 0, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.streamUI.focusID == advanced {
		t.Fatal("the wheel did not move the selection back", m.streamUI.selected)
	}
}

// An empty agent table renders a placeholder instead of a blank panel.
func TestAgentsTablePlaceholder(t *testing.T) {
	if got := (&agentsTable{}).render(0); !strings.Contains(ansi.Strip(got), "No agents.") {
		t.Fatal(got)
	}
}
