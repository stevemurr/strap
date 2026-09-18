package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

func TestWorkEventsAndAssignmentsDisplayContext(t *testing.T) {
	m, _ := setup(t)
	for _, tc := range []struct {
		event conversation.Event
		want  string
	}{
		{conversation.WorkEvent{Event: work.Event{Kind: work.PlanChanged, Plan: &work.Plan{ID: "plan", Title: "My plan", Steps: []work.Step{{Title: "first", Status: work.Pending}}}}}, "My plan"},
		{conversation.WorkEvent{Event: work.Event{Kind: work.ProgressChanged, Work: work.Work{ID: "work", Task: "task", State: work.Active, Blocker: "missing data"}}}, "Blocked: missing data"},
		{conversation.MessageEvent{Message: message.Message{From: "root", To: "child", Kind: message.Instruction, Work: &work.Work{Task: "task", Context: "context", ExpectedOutput: "report"}}}, "Expected output: report"},
		{conversation.MessageEvent{Message: message.Message{From: "child", To: message.User, Kind: message.Failure, Content: "failed"}}, "failed"},
		{conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "child", Parent: "root", State: agent.Idle}}, "Agent created"},
		{conversation.AgentExited{Agent: "child", Err: errors.New("model failure")}, "model failure"},
	} {
		m.Update(received{event: tc.event})
		if !strings.Contains(m.entries[len(m.entries)-1].body, tc.want) {
			t.Fatalf("missing %q in %+v", tc.want, m.entries)
		}
	}
	before := len(m.entries)
	m.Update(received{event: conversation.MessageEvent{Message: message.Message{From: "child", To: "root", Kind: message.Notification, Event: &work.Event{}}}})
	if len(m.entries) != before {
		t.Fatal("event rendered twice")
	}
	m.Update(received{event: conversation.AgentStateChanged{Agent: "root", State: agent.PauseRequested}})
	if !strings.Contains(m.status(), "pause requested") {
		t.Fatal(m.status())
	}
	m.Update(received{event: conversation.AgentStateChanged{Agent: "root", State: agent.Paused}})
	if !strings.Contains(m.status(), "Root paused") {
		t.Fatal(m.status())
	}
}
func TestCommandErrorsAndHistoryWithoutMessages(t *testing.T) {
	m, s := setup(t)
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	for _, command := range []string{"/transcript root extra", "/inspect root extra", "/unknown"} {
		enter(m, command)
		if got := m.entries[len(m.entries)-1]; got.label != "Error" && !strings.Contains(got.body, "Unknown command") {
			t.Fatal(got)
		}
	}
	s.err = errors.New("unavailable")
	enter(m, "hello")
	if !strings.Contains(m.entries[len(m.entries)-1].body, "unavailable") {
		t.Fatal(m.entries)
	}
	if m.Init() == nil {
		t.Fatal("missing initialization command")
	}
	m.Update(received{err: context.Canceled})
	if !strings.Contains(m.status(), "closed") {
		t.Fatal(m.status())
	}
}

type inspectionSession struct {
	*fakeSession
	inspection harness.AgentInspection
	failure    error
}

func (s *inspectionSession) InspectAgent(message.ActorID, conversation.InspectOptions) (harness.AgentInspection, error) {
	return s.inspection, s.failure
}
func TestTranscriptFormattingEmptyPagesAndErrors(t *testing.T) {
	m, base := setup(t)
	s := &inspectionSession{fakeSession: base, inspection: harness.AgentInspection{AgentInspection: conversation.AgentInspection{AgentInfo: conversation.AgentInfo{ID: "root", State: agent.Running}}}}
	m.session = s
	enter(m, "/transcript")
	if !strings.Contains(m.View(), "No transcript returned") {
		t.Fatal(m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	s.inspection.Transcript = &agent.TranscriptPage{}
	key(m, "r")
	if !strings.Contains(m.View(), "No messages") {
		t.Fatal(m.View())
	}
	s.inspection.Transcript = &agent.TranscriptPage{HasEarlier: true, Entries: []agent.TranscriptEntry{{Position: 2, Message: provider.Message{Role: "user", Envelope: &message.Message{From: "root", To: "child", Content: "task", Work: &work.Work{Task: "delegated"}, Event: &work.Event{Kind: work.WorkAssigned}}}}, {Position: 3, Message: provider.Message{Role: "tool", ToolCallID: "call", Content: content.Content{{Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2}}}}}}}}
	key(m, "r")
	key(m, "v")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	if !strings.Contains(m.transcript.viewport.View(), "Envelope") {
		t.Fatal(m.View())
	}
	key(m, "v")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !strings.Contains(m.transcript.viewport.View(), "binary data omitted") {
		t.Fatal(m.View())
	}
	s.failure = errors.New("inspection unavailable")
	key(m, "r")
	if !strings.Contains(m.View(), "inspection unavailable") {
		t.Fatal(m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.transcript.err != "inspection unavailable" {
		t.Fatal(m.transcript.err)
	}
	s.failure = nil
	s.inspection.Transcript = nil
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.transcript.err != "No transcript returned." {
		t.Fatal(m.transcript.err)
	}
	for _, k := range []tea.KeyType{tea.KeyPgDown, tea.KeyDown, tea.KeyCtrlHome, tea.KeyCtrlEnd, tea.KeyF2, tea.KeyF2} {
		m.Update(tea.KeyMsg{Type: k})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(m.View(), "COPY MODE") {
		t.Fatal(m.View())
	}
	m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 2})
	if !strings.Contains(m.View(), "Transcript") {
		t.Fatal(m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.transcript != nil {
		t.Fatal("transcript did not close")
	}
}
func TestTranscriptQuitAndForwardSwitch(t *testing.T) {
	m, _ := transcriptSetup(t)
	enter(m, "/transcript root")
	key(m, "]")
	if m.transcript.inspection.ID != "agent-7" {
		t.Fatal(m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil || m.View() != "" {
		t.Fatal("did not quit")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("quitting model still active")
	}
}
func TestTerminalTextAndElapsedFormatting(t *testing.T) {
	if got := safeText("hello\x00\x07\r\nworld\t!"); got != "hello\nworld\t!" {
		t.Fatal(got)
	}
	if elapsed(-time.Second) != "0.0s" || elapsed(125*time.Second) != "2m05s" {
		t.Fatal("incorrect elapsed time")
	}
	if toolName("_ - ") != "Tool" {
		t.Fatal("missing fallback label")
	}
}
func TestCanceledRunReturnsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, &fakeSession{events: make(chan conversation.Event)}, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleRevisionRejectsStaleEvents(t *testing.T) {
	m, _ := setup(t)
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Paused, Revision: 8})
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Running, Revision: 7})
	if m.states["root"] != agent.Paused || m.revisions["root"] != 8 {
		t.Fatal(m.states, m.revisions)
	}
}
