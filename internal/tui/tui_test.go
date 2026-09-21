package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
)

type fakeSession struct {
	sent    []string
	err     error
	events  chan conversation.Event
	managed string
}

func (s *fakeSession) Root() message.ActorID { return "root" }
func (s *fakeSession) Send(to message.ActorID, text string) (message.Receipt, error) {
	if s.err != nil {
		return message.Receipt{}, s.err
	}
	s.sent = append(s.sent, text)
	return message.Receipt{MessageID: message.MessageID(fmt.Sprint(len(s.sent))), Recipient: to, Status: message.Queued}, nil
}
func (s *fakeSession) Agents() []harness.AgentInfo {
	return []harness.AgentInfo{{AgentInfo: conversation.AgentInfo{ID: "root", Parent: message.User}}}
}
func (s *fakeSession) NextEvent(ctx context.Context) (conversation.Event, error) {
	select {
	case event := <-s.events:
		return event, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func setup(t *testing.T) (*model, *fakeSession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &fakeSession{events: make(chan conversation.Event)}
	m := newModel(ctx, cancel, s, Options{Model: "local-model", Endpoint: "localhost"})
	// These original tests exercise the combined transcript. Focused-stream
	// behavior and the default root selection are covered in streams_test.go.
	m.selectStream("")
	return m, s
}

func enter(m *model, text string) {
	m.input.SetValue(text)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestInputRemainsUsableWhileRootProcesses(t *testing.T) {
	m, s := setup(t)
	enter(m, "first")
	m.Update(received{event: conversation.AckEvent{Receipt: message.Receipt{
		MessageID: "1", Recipient: "root", Status: message.Consumed,
	}}})
	if !strings.Contains(m.status(), "processing") {
		t.Fatal(m.status())
	}
	enter(m, "steering")
	if len(s.sent) != 2 || s.sent[1] != "steering" {
		t.Fatal(s.sent)
	}
	if !strings.Contains(m.status(), "queued") {
		t.Fatal(m.status())
	}
	// An arriving reply redraws output without destroying partially typed input.
	m.input.SetValue("my next thought")
	m.Update(received{event: conversation.MessageEvent{Message: message.Message{
		From: "root", To: message.User, Kind: message.Reply, Content: "response",
	}}})
	if m.input.Value() != "my next thought" {
		t.Fatal("reply overwrote input")
	}
	if got := m.entries[len(m.entries)-1]; got.label != "Strap" || got.body != "response" {
		t.Fatal(got)
	}
}

func TestCommandsDoNotReachModelAndClearPreservesSession(t *testing.T) {
	m, s := setup(t)
	enter(m, "hello")
	enter(m, "/agents")
	if !strings.Contains(m.entries[len(m.entries)-1].body, "root") {
		t.Fatal("no agent status")
	}
	enter(m, "/help")
	if !strings.Contains(m.entries[len(m.entries)-1].body, "/quit") {
		t.Fatal("missing help")
	}
	enter(m, "/clear")
	if len(m.entries) != 0 || len(s.sent) != 1 || len(m.history) != 1 {
		t.Fatal("clear changed the conversation")
	}
	enter(m, "follow-up")
	if len(s.sent) != 2 || s.sent[1] != "follow-up" {
		t.Fatal(s.sent)
	}
}

func TestInputHistoryRestoresUnsentDraft(t *testing.T) {
	m, _ := setup(t)
	enter(m, "first")
	enter(m, "second")
	m.input.SetValue("unfinished")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "second" {
		t.Fatal(m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "first" {
		t.Fatal(m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.input.Value() != "unfinished" {
		t.Fatal(m.input.Value())
	}
}

func TestFailureVisibleAndDraftPreserved(t *testing.T) {
	m, s := setup(t)
	s.err = errors.New("cannot deliver")
	enter(m, "keep this")
	if m.input.Value() != "keep this" || !strings.Contains(m.entries[len(m.entries)-1].body, "cannot deliver") {
		t.Fatal("delivery failure lost")
	}
	m.Update(received{event: conversation.AgentExited{Agent: "root", Err: errors.New("HTTP 503: model not loaded")}})
	if !strings.Contains(m.status(), "stopped") || !strings.Contains(m.entries[len(m.entries)-1].body, "503") {
		t.Fatal("provider failure hidden")
	}
	s.err = nil
	enter(m, "do not silently enqueue")
	if len(s.sent) != 0 {
		t.Fatal("sent to stopped root")
	}
}

func TestQuitReleasesPendingEventReader(t *testing.T) {
	m, _ := setup(t)
	done := make(chan tea.Msg, 1)
	wait := m.listen()
	go func() { done <- wait() }()
	_, command := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("no quit command")
	}
	select {
	case event := <-done:
		if !errors.Is(event.(received).err, context.Canceled) {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("event reader leaked")
	}
}

// assertFits fails when any line of view is wider than width or, when height
// is positive, when view has more lines than height.
func assertFits(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if height > 0 && len(lines) > height {
		t.Fatalf("view exceeds %d lines:\n%s", height, view)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > width {
			t.Fatalf("line exceeds %d columns: %q", width, line)
		}
	}
}

func TestResizeWrapsTranscriptAndPreservesDraft(t *testing.T) {
	m, _ := setup(t)
	m.add("Strap", strings.Repeat("A longer answer with Unicode café 界. ", 20), true)
	m.input.SetValue("still typing")
	for _, size := range []tea.WindowSizeMsg{{Width: 100, Height: 30}, {Width: 40, Height: 12}, {Width: 1, Height: 1}, {Width: 80, Height: 24}} {
		m.Update(size)
		assertFits(t, m.View(), size.Width, 0)
		if m.input.Value() != "still typing" {
			t.Fatal("resize destroyed draft")
		}
	}
}

func TestRemoteTextCannotEmitTerminalControlSequences(t *testing.T) {
	m, _ := setup(t)
	m.add("Strap", "hello\x1b[2J\x1b]0;malicious title\x07world", true)
	got := m.entries[len(m.entries)-1].body
	if got != "helloworld" {
		t.Fatalf("unexpected safe text: %q", got)
	}
	if !strings.Contains(ansi.Strip(m.View()), "helloworld") {
		t.Fatal("content missing")
	}
}

func (s *fakeSession) InspectAgent(id message.ActorID, options conversation.InspectOptions) (harness.AgentInspection, error) {
	s.managed = "inspect:" + string(id)
	return harness.AgentInspection{AgentInspection: conversation.AgentInspection{AgentInfo: conversation.AgentInfo{ID: id, State: agent.Idle}}}, s.err
}
func (s *fakeSession) PauseAgent(id message.ActorID) (conversation.AgentInfo, error) {
	s.managed = "pause:" + string(id)
	return conversation.AgentInfo{ID: id, State: agent.PauseRequested}, s.err
}
func (s *fakeSession) ResumeAgent(id message.ActorID) (conversation.AgentInfo, error) {
	s.managed = "resume:" + string(id)
	return conversation.AgentInfo{ID: id, State: agent.Running}, s.err
}
func (s *fakeSession) Interrupt(context.Context) error { s.managed = "interrupt"; return s.err }

func (s *fakeSession) StopAgent(id message.ActorID) (conversation.AgentInfo, error) {
	s.managed = "stop:" + string(id)
	return conversation.AgentInfo{ID: id, State: agent.StopRequested}, s.err
}
func TestManagementCommandsAndPausedStatus(t *testing.T) {
	m, s := setup(t)
	for _, cmd := range []string{"pause", "resume", "inspect", "terminate"} {
		m.input.SetValue("/" + cmd + " agent-2")
		m.submit()
		expected := cmd
		if cmd == "terminate" {
			expected = "stop"
		}
		if s.managed != expected+":agent-2" {
			t.Fatal(s.managed)
		}
	}
	m.input.SetValue("/resume")
	m.submit()
	if s.managed != "resume:root" {
		t.Fatal(s.managed)
	}
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Paused})
	if !strings.Contains(m.status(), "Root paused") {
		t.Fatal(m.status())
	}
}
