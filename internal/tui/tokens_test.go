package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestToolCountRunsOutsideUpdateAndKeepsInputUsable(t *testing.T) {
	m, s := setup(t)
	m.entries = nil
	calls := 0
	m.session = &countingSession{s, func(ctx context.Context, id message.ActorID, revision uint64) (int64, error) {
		calls++
		if id != "root" || revision != 5 {
			t.Fatalf("wrong count target: %s %d", id, revision)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("count is not time bounded")
		}
		return 12345, nil
	}}
	addTool(m, "root", "read_file")
	addTool(m, "root", "shell")
	_, cmd := m.Update(received{event: batchEvent("root", 5, "read_file", "shell")})
	if calls != 0 || !strings.Contains(m.View(), "counting context") {
		t.Fatal("count blocked Update or pending state missing")
	}
	enter(m, "still typing")
	if len(s.sent) != 1 {
		t.Fatal("count blocked input")
	}
	commands, ok := cmd().(tea.BatchMsg)
	if !ok || len(commands) != 2 {
		t.Fatal("count stopped event listening")
	}
	m.Update(commands[1]())
	if calls != 1 || !strings.Contains(m.View(), "12,345 context tokens") {
		t.Fatal(m.View())
	}
	if again := m.countToolBatch(batchEvent("root", 5, "read_file", "shell")); again != nil {
		t.Fatal("duplicate batch recounted")
	}
}

func TestToolCountsStayWithAgentAndRevisionWhenResultsArriveOutOfOrder(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	addTool(m, "worker", "read_file")
	m.countToolBatch(batchEvent("worker", 4, "read_file"))
	addTool(m, "worker", "shell")
	m.countToolBatch(batchEvent("worker", 6, "shell"))
	addTool(m, "root", "shell")
	m.countToolBatch(batchEvent("root", 4, "shell"))
	m.Update(countedTokens{agent: "worker", revision: 6, count: 2400})
	m.Update(countedTokens{agent: "root", revision: 4, count: 600})
	m.Update(countedTokens{agent: "worker", revision: 4, count: 1000})
	got := ansi.Strip(m.View())
	for _, want := range []string{"worker · Read file, Shell · 2,400 context tokens", "root · Shell · 600 context tokens"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "1,000") || strings.Contains(got, "3,400") {
		t.Fatal("context snapshots summed or regressed", got)
	}
	m.add("Strap", "message separates batches", false)
	addTool(m, "worker", "shell")
	m.countToolBatch(batchEvent("worker", 9, "shell"))
	m.Update(countedTokens{agent: "worker", revision: 9, count: 9000})
	if got := m.View(); !strings.Contains(got, "2,400") || !strings.Contains(got, "9,000") {
		t.Fatal("historical row changed", got)
	}
}

func TestTokenCountsRespectFreezeResizeAndClear(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	addTool(m, "root", "a_very_long_tool_name_that_would_hide_the_count")
	event := batchEvent("root", 4, "a_very_long_tool_name_that_would_hide_the_count")
	m.countToolBatch(event)
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(countedTokens{agent: "root", revision: 4, count: 12345})
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
	m.Update(countedTokens{agent: "root", revision: 4, count: 777})
	if len(m.entries) != 0 || m.countToolBatch(event) != nil {
		t.Fatal("late telemetry resurrected cleared tools")
	}
}

func TestToolCountZeroAndFailureAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		count int64
		err   error
		want  string
	}{
		{0, nil, "0 context tokens"},
		{0, errors.New("offline"), "context tokens unavailable"},
		{-1, nil, "context tokens unavailable"},
	} {
		m, _ := setup(t)
		m.entries = nil
		addTool(m, "root", "shell")
		m.countToolBatch(batchEvent("root", 4, "shell"))
		m.Update(countedTokens{agent: "root", revision: 4, count: tc.count, err: tc.err})
		if !strings.Contains(m.View(), tc.want) {
			t.Fatal(m.View())
		}
	}
	m, _ := setup(t)
	addTool(m, "root", "shell")
	cmd := m.countToolBatch(batchEvent("root", 4, "shell"))
	m.Update(cmd()) // A session without counting support remains usable.
	if !strings.Contains(m.View(), "context tokens unavailable") {
		t.Fatal(m.View())
	}
}

func TestQuitCancelsTokenCount(t *testing.T) {
	m, s := setup(t)
	started := make(chan struct{})
	m.session = &countingSession{s, func(ctx context.Context, _ message.ActorID, _ uint64) (int64, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	addTool(m, "root", "shell")
	cmd := m.countToolBatch(batchEvent("root", 4, "shell"))
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("count did not start")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case result := <-done:
		if !errors.Is(result.(countedTokens).err, context.Canceled) {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("token count leaked after quit")
	}
}
