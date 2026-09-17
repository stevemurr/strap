package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func TestDebugTraceShowsDelegationMessagesAndToolNames(t *testing.T) {
	m, _ := setup(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.observe(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: "worker", Parent: "root", State: agent.Idle}})
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "job", From: "root", To: "worker", Kind: message.Instruction, Work: &work.Work{Task: "Read a page", Context: "Background", ExpectedOutput: "Summary"}}})
	activity := agent.ToolActivity{Call: provider.ToolCall{ID: "read-1", Name: "read_pdf", Arguments: []byte(`{"path":"notes.pdf"}`)}, StartedAt: now}
	m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
	if !m.busy() {
		t.Fatal("worker tool did not activate spinner")
	}
	now = now.Add(2300 * time.Millisecond)
	activity.FinishedAt = now
	activity.Result = tool.Result{Content: content.Content{{Text: `{"pages":1}`}, {Image: &content.Image{MIMEType: "image/png", Data: []byte("not-for-display")}}}}
	m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "answer", From: "worker", To: "root", Kind: message.Reply, ReplyTo: "job", Content: "Page summary"}})
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "steer", From: "root", To: "worker", Kind: message.Instruction, Content: "Also read page two"}})
	var transcript strings.Builder
	for _, e := range m.entries {
		transcript.WriteString(e.label + " " + e.meta + "\n" + e.body + "\n")
	}
	for _, want := range []string{"Delegation root → worker", "Task: Read a page", "Context: Background", "Expected output: Summary", "Read PDF", "worker → root · reply · answer · reply to job", "Page summary", "Also read page two"} {
		if !strings.Contains(transcript.String(), want) {
			t.Errorf("missing %q in %s", want, transcript.String())
		}
	}
	if strings.Contains(transcript.String(), "not-for-display") {
		t.Fatal("dumped image bytes")
	}
	if len(m.activeTools) != 0 {
		t.Fatal("finished tool remains active")
	}
}

func TestSpinnerAndTimerFollowActivityIncludingDelegatedWork(t *testing.T) {
	m, _ := setup(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	enter(m, "do work")
	before := m.spinner.View()
	_, nextTick := m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if before == m.spinner.View() || nextTick == nil {
		t.Fatal("spinner did not advance and schedule next tick")
	}
	now = now.Add(3400 * time.Millisecond)
	if !strings.Contains(m.activityLine(), "3.4s") {
		t.Fatal(m.activityLine())
	}
	m.observe(conversation.AckEvent{Receipt: message.Receipt{MessageID: "1", Recipient: "root", Status: message.Consumed}})
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Running})
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Idle})
	if !m.busy() || !strings.Contains(m.activityLine(), "1 agent(s) processing") {
		t.Fatal("root idle hid worker activity")
	}
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Idle})
	if m.busy() || !strings.Contains(m.activityLine(), "last active 3.4s") {
		t.Fatal(m.activityLine())
	}
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Paused})
	enter(m, "queued while paused")
	if m.busy() {
		t.Fatal("paused inbox should not spin")
	}
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Running})
	if !m.busy() || !strings.Contains(m.activityLine(), "0.0s") {
		t.Fatal("resume did not start new timer")
	}
	m.Update(received{err: context.Canceled})
	if m.busy() {
		t.Fatal("closed conversation still spins")
	}
}

func TestToolErrorsCancellationAndTerminalText(t *testing.T) {
	m, _ := setup(t)
	activity := agent.ToolActivity{Call: provider.ToolCall{ID: "call", Name: "shell\x1b[2J", Arguments: []byte(`{}`)}, StartedAt: time.Now()}
	m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
	if strings.Contains(m.activityLine(), "\x1b[2J") {
		t.Fatal("tool name injected terminal controls")
	}
	activity.FinishedAt = activity.StartedAt.Add(time.Second)
	activity.Err = errors.New("execution canceled")
	m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
	last := m.entries[len(m.entries)-1]
	if last.label != "Error" || last.body != "Shell failed: execution canceled" || strings.Contains(last.meta, "\x1b") {
		t.Fatal(last)
	}
	activity.FinishedAt = time.Time{}
	m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
	m.observe(conversation.AgentExited{Agent: "worker", Err: context.Canceled})
	if m.busy() || len(m.activeTools) != 0 {
		t.Fatal("exited agent kept tool active")
	}
}

func TestFreezePreservesSelectionWhileEventsContinue(t *testing.T) {
	m, s := setup(t)
	if !m.viewport.MouseWheelEnabled {
		t.Fatal("mouse scrolling should be enabled")
	}
	m.input.SetValue("draft")
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	if !strings.Contains(frozen, "DISPLAY FROZEN") {
		t.Fatal("freeze not indicated")
	}
	_, listen := m.Update(received{event: conversation.MessageEvent{Message: message.Message{From: "root", To: message.User, Kind: message.Reply, Content: "arrived while frozen"}}})
	if listen == nil {
		t.Fatal("freeze stopped listening")
	}
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ignored")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.View() != frozen || m.input.Value() != "draft" || len(s.sent) != 0 {
		t.Fatal("freeze changed display or submitted input")
	}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 25})
	if strings.Contains(m.View(), "arrived while frozen") {
		t.Fatal("resize revealed buffered entries")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	if strings.Contains(m.View(), "arrived while frozen") {
		t.Fatal("scroll revealed buffered entries")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !strings.Contains(m.View(), "arrived while frozen") || m.input.Value() != "draft" {
		t.Fatal("unfreeze lost events or draft")
	}
}

func TestTimerSurvivesReplyHandoff(t *testing.T) {
	m, _ := setup(t)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Running})
	now = now.Add(10 * time.Second)
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "reply", From: "worker", To: "root", Kind: message.Reply, Content: "result"}})
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Idle})
	if !m.busy() {
		t.Fatal("reply awaiting root consumption ended activity")
	}
	m.observe(conversation.AckEvent{Receipt: message.Receipt{MessageID: "reply", Recipient: "root", Status: message.Consumed}})
	now = now.Add(time.Second)
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "done", From: "root", To: message.User, Kind: message.Reply, Content: "done"}})
	m.observe(conversation.AckEvent{Receipt: message.Receipt{MessageID: "done", Recipient: message.User, Status: message.Queued}})
	if m.busy() || !strings.Contains(m.activityLine(), "last active 11.0s") {
		t.Fatal(m.activityLine())
	}
}

func TestQueuedInstructionForPausedAgentDoesNotSpin(t *testing.T) {
	m, _ := setup(t)
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Paused})
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "job", From: "root", To: "worker", Kind: message.Instruction, Content: "queued"}})
	if m.busy() {
		t.Fatal("paused worker inbox should not spin")
	}
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Running})
	if !m.busy() {
		t.Fatal("resumed worker should spin")
	}
}

func TestCompletedExchangeShowsIdleWithoutReceiptNoise(t *testing.T) {
	m, _ := setup(t)
	enter(m, "hello")
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "1", From: message.User, To: "root", Kind: message.Instruction, Content: "hello"}})
	m.observe(conversation.AckEvent{Receipt: message.Receipt{MessageID: "1", Recipient: "root", Status: message.Queued}})
	m.observe(conversation.AckEvent{Receipt: message.Receipt{MessageID: "1", Recipient: "root", Status: message.Consumed}})
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "2", From: "root", To: message.User, Kind: message.Reply, Content: "hi"}})
	m.observe(conversation.AckEvent{Receipt: message.Receipt{MessageID: "2", Recipient: message.User, Status: message.Queued}})
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Idle})
	if m.busy() || len(m.pending) != 0 || m.status() != "Idle" {
		t.Fatal(m.status())
	}
	for _, e := range m.entries {
		if e.label == "Delivery" {
			t.Fatal("receipt status presented as conversation output")
		}
	}
	if got := m.entries[len(m.entries)-1]; got.body != "hi" {
		t.Fatal(got)
	}
}

func TestToolTimelineShowsPreviewsButHidesRawDetails(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	for _, name := range []string{"create_agent", "read_pdf", "some_custom_tool"} {
		activity := agent.ToolActivity{Call: provider.ToolCall{ID: "raw-call-id", Name: name, Arguments: []byte(`{"path":"private.pdf"}`)}, StartedAt: time.Now()}
		m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
		activity.FinishedAt = activity.StartedAt.Add(time.Second)
		activity.Result = tool.Text("raw-result")
		m.observe(conversation.ToolEvent{Agent: "worker", Activity: activity})
	}
	got := m.View()
	for _, want := range []string{"Create agent", "Read private.pdf", "Some custom tool", "raw-result"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, raw := range []string{"raw-call-id", "create_agent", "read_pdf"} {
		if strings.Contains(got, raw) {
			t.Errorf("raw tool information shown: %s", raw)
		}
	}
	if len(m.entries) != 3 {
		t.Fatalf("expected one entry per call, got %d", len(m.entries))
	}
}
