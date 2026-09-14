package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
)

type countingSession struct {
	*fakeSession
	count func(context.Context, message.ActorID, uint64) (int64, error)
}

func (s *countingSession) CountAgentTokens(ctx context.Context, id message.ActorID, revision uint64) (int64, error) {
	return s.count(ctx, id, revision)
}

func batchEvent(id message.ActorID, revision uint64, calls ...string) conversation.ToolBatchEvent {
	return conversation.ToolBatchEvent{Agent: id, Batch: agent.ToolBatch{ContextRevision: revision, Calls: calls}}
}

func TestToolCountsAreObservedWithoutProviderCalls(t *testing.T) {
	m, s := setup(t)
	m.entries = nil
	m.resize(140, 100)
	calls := 0
	m.session = &countingSession{s, func(context.Context, message.ActorID, uint64) (int64, error) { calls++; return 12345, nil }}
	addTool(m, "root", "read_file")
	addTool(m, "root", "shell")
	m.Update(received{event: batchEvent("root", 5, "read_file", "shell")})
	expandActivityForTest(m, true)
	if calls != 0 || !strings.Contains(m.View(), "counting context") {
		t.Fatal("batch did not register passive measurement")
	}
	enter(m, "still typing")
	if len(s.sent) != 1 {
		t.Fatal("input blocked")
	}
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "root", Revision: 5, Count: 12345}})
	expandActivityForTest(m, true)
	if calls != 0 || !strings.Contains(m.View(), "12,345 context tokens") {
		t.Fatal(m.View())
	}
}

func TestToolCountsStayWithAgentAndRevisionWhenResultsArriveOutOfOrder(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(140, 100)
	addTool(m, "worker", "read_file")
	m.Update(received{event: batchEvent("worker", 4, "read_file")})
	addTool(m, "worker", "shell")
	m.Update(received{event: batchEvent("worker", 6, "shell")})
	addTool(m, "root", "shell")
	m.Update(received{event: batchEvent("root", 4, "shell")})
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "worker", Revision: 6, Count: 2400}})
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "root", Revision: 4, Count: 600}})
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "worker", Revision: 4, Count: 1000}})
	expandActivityForTest(m, true)
	got := ansi.Strip(m.View())
	for _, want := range []string{"1,000 context tokens", "2,400 context tokens", "600 context tokens"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "3,400") {
		t.Fatal("context snapshots summed or regressed", got)
	}
	m.add("Strap", "message separates batches", false)
	addTool(m, "worker", "shell")
	m.Update(received{event: batchEvent("worker", 9, "shell")})
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "worker", Revision: 9, Count: 9000}})
	expandActivityForTest(m, true)
	if got := m.View(); !strings.Contains(got, "2,400") || !strings.Contains(got, "9,000") {
		t.Fatal("historical row changed", got)
	}
}

func TestTokenCountsRespectFreezeResizeAndClear(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(140, 100)
	addTool(m, "root", "a_very_long_tool_name_that_would_hide_the_count")
	event := batchEvent("root", 4, "a_very_long_tool_name_that_would_hide_the_count")
	m.Update(received{event: event})
	expandActivityForTest(m, true)
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "root", Revision: 4, Count: 12345}})
	if m.View() != frozen {
		t.Fatal("count changed frozen display")
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	if strings.Contains(m.View(), "12,345") {
		t.Fatal("resize revealed a frozen update")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(m.View(), "12,345 context tokens") {
		t.Fatal("resume lost count", m.View())
	}
	for _, width := range []int{50, 25, 1, 80} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		for _, line := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("row exceeds %d: %q", width, line)
			}
		}
	}
	enter(m, "/clear")
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "root", Revision: 4, Count: 777}})
	m.Update(received{event: event})
	if len(m.entries) != 0 {
		t.Fatal("late telemetry resurrected cleared tools")
	}
}

func TestToolCountZeroAndFailureAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		count int64
		err   string
		want  string
	}{
		{0, "", "0 context tokens"},
		{0, "offline", "context tokens unavailable"},
		{-1, "", "context tokens unavailable"},
	} {
		m, _ := setup(t)
		m.entries = nil
		m.resize(140, 100)
		addTool(m, "root", "shell")
		m.Update(received{event: batchEvent("root", 4, "shell")})
		m.Update(received{event: conversation.ContextTokensEvent{Agent: "root", Revision: 4, Count: tc.count, Error: tc.err}})
		expandActivityForTest(m, true)
		if !strings.Contains(m.View(), tc.want) {
			t.Fatal(m.View())
		}
	}
	m, _ := setup(t)
	addTool(m, "root", "shell")
	m.Update(received{event: batchEvent("root", 4, "shell")})
	m.Update(received{event: conversation.ContextTokensEvent{Agent: "root", Revision: 4, Error: "counting unavailable"}})
	expandActivityForTest(m, true)
	if !strings.Contains(m.View(), "context tokens unavailable") {
		t.Fatal(m.View())
	}
}
