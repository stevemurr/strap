package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
)

func TestEscapeInterruptsInFlightWorkWithoutLosingDraft(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(*model)
	}{
		{"root", func(m *model) { m.working["root"] = true }},
		{"delegate", func(m *model) { m.working["worker"] = true }},
		{"tool", func(m *model) { m.activeTools[toolKey{agent: "root", call: "shell-1"}] = agent.ToolActivity{} }},
		{"queued request", func(m *model) { m.pending["request-1"] = "root" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s := setup(t)
			tc.start(m)
			m.input.SetValue("unfinished\ndraft")
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if cmd == nil || !m.interrupting || s.managed != "" {
				t.Fatal("Escape did not schedule asynchronous interruption")
			}
			entries := len(m.entries)
			_, duplicate := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if duplicate != nil || len(m.entries) != entries {
				t.Fatal("repeated Escape duplicated interruption")
			}
			m.Update(cmd())
			if s.managed != "interrupt" || m.interrupting || m.quitting || m.rootStopped || m.ctx.Err() != nil {
				t.Fatal("Escape closed the session or failed to interrupt")
			}
			if m.input.Value() != "unfinished\ndraft" || len(s.sent) != 0 {
				t.Fatal("Escape changed or sent the draft")
			}
		})
	}
}

func TestEscapeDismissesUIBeforeInterrupting(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*model)
	}{
		{"completion", func(m *model) { m.input.SetValue("/ag") }},
		{"agent focus", func(m *model) { m.focusRoster(true) }},
		{"agent hover", func(m *model) { m.streamUI.hovering = true }},
		{"activity focus", func(m *model) { m.folds.focused = true }},
		{"text selection", func(m *model) { m.mouseSelection = &mouseSelection{} }},
		{"history inspector", func(m *model) { m.transcript = &transcriptView{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, s := setup(t)
			m.working["root"] = true
			tc.open(m)
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if m.interrupting || s.managed != "" {
				t.Fatal("dismissal interrupted work")
			}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if cmd == nil || !m.interrupting {
				t.Fatal("Escape after dismissal did not interrupt")
			}
		})
	}
}

func TestEscapeDoesNothingWhenIdleOrFrozen(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		m, s := setup(t)
		if frozen {
			m.working["root"] = true
			m.toggleSelection()
		}
		m.input.SetValue("draft")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if cmd != nil || m.interrupting || s.managed != "" || m.input.Value() != "draft" {
			t.Fatal("idle or frozen Escape changed the session")
		}
	}
}

func TestEscapeInterruptionFailureCanBeRetried(t *testing.T) {
	m, s := setup(t)
	m.working["root"] = true
	s.err = errors.New("interruption failed")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(cmd())
	if m.interrupting || !strings.Contains(m.View(), "interruption failed") {
		t.Fatal("failure left the UI stuck stopping")
	}
	s.err = nil
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("interruption could not be retried")
	}
	m.Update(cmd())
	if m.interrupting || m.quitting {
		t.Fatal("retry did not settle")
	}
}

func TestStopInterruptsSessionAndTerminateRemainsExplicit(t *testing.T) {
	m, s := setup(t)
	m.input.SetValue("/stop")
	_, cmd := m.submit()
	if cmd == nil || !m.interrupting {
		t.Fatal("stop did not start asynchronous interruption")
	}
	if !strings.Contains(m.status(), "Stopping") {
		t.Fatal(m.status())
	}
	m.Update(cmd())
	if s.managed != "interrupt" || m.interrupting || m.rootStopped {
		t.Fatal("stop terminated root or failed to settle")
	}
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Interrupted})
	if !strings.Contains(m.status(), "new instruction") {
		t.Fatal(m.status())
	}
	m.input.SetValue("/stop root")
	m.submit()
	if s.managed != "interrupt" {
		t.Fatal("old stop syntax terminated agent")
	}
	m.input.SetValue("/terminate root")
	m.submit()
	if s.managed != "stop:root" {
		t.Fatal("explicit termination was lost")
	}
}
