package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func manyAgents(t *testing.T) *model {
	t.Helper()
	m := activitySetup(t)
	tasks := []string{"Agent list layout", "Keyboard navigation", "Stream scroll anchors", "Turn compression", "Transcript inspection", "Markdown wrapping", "Review agent grouping", "Audit turn folding", "Unread counts", "Composer routing", "Narrow terminal layout"}
	for i, task := range tasks {
		id := message.ActorID(fmt.Sprintf("agent-%d", i+1))
		m.ensureStream(id).parent = "root"
		m.states[id] = agent.Idle
		w := work.Work{ID: work.ID(fmt.Sprintf("w%d", i)), Owner: "root", Assignee: id, Kind: work.Implementation, State: work.Active, Task: task}
		if i == 2 {
			w.Blocker = "Need a captured regression"
		}
		if i == 6 {
			w.State = work.NeedsCheck
			w.Kind = work.AuditWork
		}
		if i == 5 || i >= 8 {
			w.State = work.Accepted
		}
		if i == 0 || i == 3 || i == 7 {
			m.states[id] = agent.Running
			m.working[id] = true
		}
		m.rememberWork(w)
	}
	m.working["root"] = true
	m.states["root"] = agent.Running
	return m
}

func TestTaskFirstRosterWithManyAgents(t *testing.T) {
	m := manyAgents(t)
	view := ansi.Strip(m.View())
	for _, want := range []string{"Needs attention  2", "Working  3", "Idle  2", "Completed  4", "Stream scroll anchors", "Review agent grouping", "Agent list layout", "Keyboard navigation"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if !m.streamUI.completedExpanded || !strings.Contains(view, "Markdown wrapping") {
		t.Fatal("completed work was hidden by default")
	}
	choices := m.rosterChoices()
	want := []message.ActorID{"", "root", "agent-3", "agent-7", "agent-1", "agent-4", "agent-8", "agent-2", "agent-5"}
	for i, id := range want {
		if choices[i].id != id {
			t.Fatalf("unstable group order: %+v", choices)
		}
	}
	m.focusRoster(true)
	for _, id := range want[2:] {
		m.moveStream(1)
		if m.streamUI.selected != id {
			t.Fatalf("keyboard skipped %s", id)
		}
	}
	m.moveStream(1)
	if !m.streamUI.completedFocused {
		t.Fatal("completed disclosure is not keyboard accessible")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.streamUI.completedExpanded {
		t.Fatal("Enter did not collapse completed agents")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.moveStream(1)
	if m.streamUI.selected != "agent-6" {
		t.Fatal("completed agents did not expand")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.streamUI.completedExpanded || !m.streamUI.completedFocused {
		t.Fatal("collapse lost selection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	enter(m, "/focus agent-11")
	if !m.streamUI.completedExpanded || m.streamUI.selected != "agent-11" {
		t.Fatal("direct focus did not reveal completed agent")
	}
}

func TestCompletedAgentsWithNewWorkOrErrorsRemainVisible(t *testing.T) {
	m := manyAgents(t)
	m.observe(conversation.AgentStateChanged{Agent: "agent-6", State: agent.Running, Revision: 2})
	if m.rosterGroup("agent-6") != "Working" {
		t.Fatal("running agent hidden in completed")
	}
	m.ensureStream("agent-9").err = "provider failed"
	if m.rosterGroup("agent-9") != "Needs attention" {
		t.Fatal("error hidden in completed")
	}
	m.rememberWork(work.Work{ID: "next", Assignee: "agent-10", State: work.Active, Task: "Follow-up"})
	if m.rosterGroup("agent-10") != "Idle" {
		t.Fatal("unfinished idle work treated as completed")
	}
	m.states["agent-11"] = agent.Paused
	if m.rosterStatus("agent-11") != "completed" || !strings.Contains(m.streamState("agent-11"), "paused") {
		t.Fatal("work and execution state were conflated")
	}
	m.selectStream("agent-1")
	m.observe(conversation.AgentStateChanged{Agent: "agent-1", State: agent.Idle, Revision: 3})
	w, _ := m.streamWork("agent-1")
	w.State = work.Accepted
	w.Revision++
	m.rememberWork(w)
	view := m.View()
	if !m.streamUI.completedExpanded || !strings.Contains(view, "Agent list layout") {
		t.Fatal("finishing work hid the selected agent")
	}
}

func TestGroupedRosterMouseAndNarrowNavigation(t *testing.T) {
	m := manyAgents(t)
	m.toggleCompleted()
	x, y := screenLocation(t, m.View(), "▸ Completed")
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	if !m.streamUI.completedExpanded {
		t.Fatal("mouse did not expand completed agents")
	}
	m.input.SetValue("a draft\nwith another line")
	for _, size := range []tea.WindowSizeMsg{{Width: 124, Height: 38}, {Width: 100, Height: 24}, {Width: 80, Height: 24}, {Width: 40, Height: 12}, {Width: 15, Height: 8}, {Width: 1, Height: 1}} {
		m.Update(size)
		m.selectStream("agent-11")
		m.focusRoster(true)
		view := m.View()
		if len(strings.Split(view, "\n")) > size.Height {
			t.Fatal("vertical overflow")
		}
		for _, row := range strings.Split(view, "\n") {
			if ansi.StringWidth(row) > size.Width {
				t.Fatal("horizontal overflow")
			}
		}
		if size.Width >= 40 && !strings.Contains(view, "agent-11") {
			t.Fatalf("selection disappeared at %+v:\n%s", size, view)
		}
	}
	if m.input.Value() != "a draft\nwith another line" {
		t.Fatal("navigation changed draft")
	}
}
