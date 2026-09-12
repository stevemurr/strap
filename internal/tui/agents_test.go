package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

type agentTableSession struct {
	*fakeSession
	inspections []conversation.AgentInspection
	count       func(context.Context, message.ActorID, uint64) (int64, error)
}

func (s *agentTableSession) Agents() []conversation.AgentInfo {
	var infos []conversation.AgentInfo
	for _, in := range s.inspections {
		infos = append(infos, in.AgentInfo)
	}
	return infos
}

func (s *agentTableSession) InspectAgent(id message.ActorID, _ conversation.InspectOptions) (conversation.AgentInspection, error) {
	for _, in := range s.inspections {
		if in.ID == id {
			return in, nil
		}
	}
	return conversation.AgentInspection{}, errors.New("unknown agent")
}

func (s *agentTableSession) CountAgentTokens(ctx context.Context, id message.ActorID, revision uint64) (int64, error) {
	return s.count(ctx, id, revision)
}

func tableInspection(id message.ActorID, revision uint64, output, limit int64) conversation.AgentInspection {
	return conversation.AgentInspection{
		AgentInfo:       conversation.AgentInfo{ID: id, Parent: "root", State: agent.Idle},
		ContextRevision: revision, OutputTokenLimit: &limit,
		Usage: agent.UsageSnapshot{OutputTokens: output * 2, Latest: &agent.UsageObservation{Usage: &provider.Usage{OutputTokens: &output}}},
	}
}

func TestAgentsCommandCountsSnapshotAndShowsPerAgentLimits(t *testing.T) {
	m, fake := setup(t)
	calls := 0
	m.session = &agentTableSession{fakeSession: fake, inspections: []conversation.AgentInspection{
		tableInspection("root", 3, 1482, 32768), tableInspection("agent-1", 7, 624, 8192),
	}, count: func(ctx context.Context, id message.ActorID, revision uint64) (int64, error) {
		calls++
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("unbounded count")
		}
		if id == "root" && revision == 3 {
			return 12345, nil
		}
		if id == "agent-1" && revision == 7 {
			return 8210, nil
		}
		t.Fatalf("incorrect snapshot: %s %d", id, revision)
		return 0, nil
	}}
	m.input.SetValue("/agents")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 0 || !strings.Contains(m.entries[len(m.entries)-1].body, "counting…") {
		t.Fatal("count blocked input or pending table missing")
	}
	enter(m, "keep working")
	if len(fake.sent) != 1 {
		t.Fatal("input blocked")
	}
	commands := cmd().(tea.BatchMsg)
	// Completions can arrive in either order.
	m.Update(commands[1]())
	m.Update(commands[0]())
	table := m.entries[len(m.entries)-2].agents
	if calls != 2 || table.rows[0][3] != "12,345" || table.rows[1][3] != "8,210" {
		t.Fatal(table)
	}
	if table.rows[0][4] != "1,482" || table.rows[1][4] != "624" {
		t.Fatal("displayed cumulative output", table)
	}
	if table.rows[0][5] != "32,768" || table.rows[1][5] != "8,192" {
		t.Fatal("lost distinct limits", table)
	}
	for _, width := range []int{120, 80, 45, 1} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 50})
		for _, line := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("overflow at %d: %s", width, line)
			}
		}
		if width == 120 && !strings.Contains(m.View(), "Output cap") {
			t.Fatal(m.View())
		}
		if width == 45 && !strings.Contains(m.View(), "Context: 12,345") {
			t.Fatal("missing narrow layout", m.View())
		}
	}
}

func TestAgentsUnknownAndZeroCounts(t *testing.T) {
	m, fake := setup(t)
	zero := tableInspection("zero", 1, 0, 100)
	missing := tableInspection("missing", 1, 10, 100)
	missing.OutputTokenLimit = nil
	missing.Usage.Latest.Usage = nil
	uncalled := tableInspection("uncalled", 1, 0, 100)
	uncalled.Usage = agent.UsageSnapshot{}
	m.session = &agentTableSession{fakeSession: fake, inspections: []conversation.AgentInspection{zero, missing, uncalled}, count: func(_ context.Context, id message.ActorID, _ uint64) (int64, error) {
		if id == "zero" {
			return 0, nil
		}
		if id == "uncalled" {
			return -1, nil
		}
		return 0, errors.New("offline")
	}}
	cmd := m.showAgents()
	for _, command := range cmd().(tea.BatchMsg) {
		m.Update(command())
	}
	rows := m.entries[len(m.entries)-1].agents.rows
	if rows[0][3] != "0" || rows[0][4] != "0" || rows[1][3] != "unknown" || rows[1][4] != "unknown" || rows[1][5] != "unknown" || rows[2][3] != "unknown" || rows[2][4] != "—" {
		t.Fatal(rows)
	}
	// Sessions without token counting still render useful state and unknown caps.
	m.session = fake
	if cmd := m.showAgents(); cmd != nil {
		t.Fatal("counted unsupported session")
	}
	if row := m.entries[len(m.entries)-1].agents.rows[0]; row[3] != "unknown" || row[5] != "unknown" {
		t.Fatal(row)
	}
}

func TestAgentsCountsRespectFreezeClearAndSeparateTables(t *testing.T) {
	m, fake := setup(t)
	m.session = &agentTableSession{fakeSession: fake, inspections: []conversation.AgentInspection{tableInspection("root", 3, 20, 100)}, count: func(context.Context, message.ActorID, uint64) (int64, error) { return 42, nil }}
	first := m.showAgents()
	second := m.showAgents()
	// A single-command Batch returns the command directly.
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	m.Update(first())
	if m.View() != frozen {
		t.Fatal("count changed frozen view")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	if strings.Contains(m.View(), "42") {
		t.Fatal("resize exposed frozen count")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if m.entries[1].agents.rows[0][3] != "42" || m.entries[2].agents.rows[0][3] != "counting…" {
		t.Fatal("result updated the wrong table")
	}
	enter(m, "/clear")
	third := m.showAgents()
	m.Update(second())
	if len(m.entries) != 1 || m.entries[0].agents.rows[0][3] != "counting…" {
		t.Fatal("late count resurrected or overwrote table")
	}
	m.Update(third())
	if m.entries[0].agents.rows[0][3] != "42" {
		t.Fatal("new table did not complete")
	}
}

func TestQuitCancelsAgentTableCount(t *testing.T) {
	m, fake := setup(t)
	started := make(chan struct{})
	m.session = &agentTableSession{fakeSession: fake, inspections: []conversation.AgentInspection{tableInspection("root", 3, 20, 100)}, count: func(ctx context.Context, _ message.ActorID, _ uint64) (int64, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	cmd := m.showAgents()
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
		if !errors.Is(result.(agentTableCount).err, context.Canceled) {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("count leaked")
	}
}
