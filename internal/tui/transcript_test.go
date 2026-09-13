package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

type transcriptSession struct {
	*fakeSession
	queries []agent.TranscriptQuery
}

func (s *transcriptSession) Agents() []harness.AgentInfo {
	return []harness.AgentInfo{{AgentInfo: conversation.AgentInfo{ID: "root"}}, {AgentInfo: conversation.AgentInfo{ID: "agent-7", Parent: "root"}}}
}
func (s *transcriptSession) InspectAgent(id message.ActorID, options conversation.InspectOptions) (harness.AgentInspection, error) {
	if id != "root" && id != "agent-7" {
		return harness.AgentInspection{}, fmt.Errorf("unknown agent: %s", id)
	}
	in := harness.AgentInspection{AgentInspection: conversation.AgentInspection{AgentInfo: conversation.AgentInfo{ID: id, Parent: "root", State: agent.Running}}}
	if options.Transcript == nil {
		return in, nil
	}
	q := *options.Transcript
	s.queries = append(s.queries, q)
	end := 25
	if q.Before != 0 {
		end = int(q.Before) - 1
	}
	start := max(0, end-20)
	page := agent.TranscriptPage{Entries: []agent.TranscriptEntry{}, HasEarlier: start > 0}
	for i := start; i < end; i++ {
		page.Entries = append(page.Entries, agent.TranscriptEntry{Position: uint64(i + 1), Message: provider.Message{Role: "assistant", Content: content.Text(fmt.Sprintf("%s message %d", id, i+1))}})
	}
	in.Transcript = &page
	return in, nil
}
func transcriptSetup(t *testing.T) (*model, *transcriptSession) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &transcriptSession{fakeSession: &fakeSession{events: make(chan conversation.Event)}}
	m := newModel(ctx, cancel, s, Options{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	return m, s
}
func key(m *model, s string) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

func TestTranscriptCommandPagingSwitchAndNoDelivery(t *testing.T) {
	m, s := transcriptSetup(t)
	enter(m, "/transcript agent-7")
	if m.transcript == nil || !strings.Contains(m.View(), "agent-7 message 25") {
		t.Fatal(m.View())
	}
	if s.queries[0].Limit != 0 || s.queries[0].Before != 0 {
		t.Fatal("not latest default page")
	}
	m.transcript.viewport.GotoTop()
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if len(s.queries) != 2 || s.queries[1].Before != 6 || len(m.transcript.inspection.Transcript.Entries) != 25 {
		t.Fatal("older messages were not prepended")
	}
	key(m, "[")
	if m.transcript.inspection.ID != "root" {
		t.Fatal("agent switch failed")
	}
	key(m, "r")
	if s.queries[len(s.queries)-1].Before != 0 {
		t.Fatal("refresh did not select latest")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.transcript != nil || len(s.sent) != 0 || len(m.history) != 0 {
		t.Fatal("browsing sent an agent message or changed input history")
	}
	enter(m, "/transcript missing")
	if m.transcript != nil || !strings.Contains(m.entries[len(m.entries)-1].body, "unknown agent") {
		t.Fatal("missing agent not reported")
	}
}

func TestTranscriptKeepsMainDraftViewportAndEventHandling(t *testing.T) {
	m, s := transcriptSetup(t)
	for i := 0; i < 30; i++ {
		m.add("Strap", fmt.Sprintf("main message %d", i), true)
	}
	m.viewport.GotoTop()
	offset := m.viewport.YOffset
	m.input.SetValue("unfinished draft")
	m.openTranscript("agent-7")
	_, cmd := m.Update(received{event: conversation.MessageEvent{Message: message.Message{From: "root", To: message.User, Kind: message.Reply, Content: "arrived while browsing"}}})
	if cmd == nil || !strings.Contains(m.entries[len(m.entries)-1].body, "arrived while browsing") {
		t.Fatal("event reader did not continue")
	}
	if strings.Contains(m.View(), "arrived while browsing") {
		t.Fatal("main events overwrote selected transcript")
	}
	key(m, "]")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 15})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.Value() != "unfinished draft" || m.viewport.YOffset != offset || len(s.sent) != 0 {
		t.Fatal("view lost draft/scroll or sent input")
	}
}

func TestTranscriptDisplaysArgumentsErrorsAndImageMetadataSafely(t *testing.T) {
	m, _ := transcriptSetup(t)
	m.openTranscript("agent-7")
	m.transcript.inspection.Transcript = &agent.TranscriptPage{Entries: []agent.TranscriptEntry{
		{Position: 1, Message: provider.Message{Role: "assistant", Content: content.Text("Checking now.\x1b]0;evil\x07"), ToolCalls: []provider.ToolCall{{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"broken":`)}}}},
		{Position: 2, Message: provider.Message{Role: "tool", ToolCallID: "c1", Content: content.Content{{Text: "Tool error: invalid JSON"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}}},
	}}
	m.renderAgentTranscript()
	text := m.transcript.viewport.View()
	for _, want := range []string{"Checking now.", `{"broken":`, "Tool error: invalid JSON", "image/png"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "evil") {
		t.Fatal("terminal control leaked")
	}
	key(m, "v")
	m.transcript.viewport.GotoTop()
	raw := m.transcript.viewport.View()
	if !strings.Contains(raw, "Checking now.") || !strings.Contains(raw, "Arguments") {
		t.Fatal("raw fields dropped invalid arguments")
	}
}
