package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

func focusedSetup(t *testing.T) (*model, *fakeSession) {
	t.Helper()
	m, s := setup(t)
	m.selectStream(s.Root())
	m.resize(124, 34)
	for _, id := range []message.ActorID{"agent-2", "agent-3"} {
		m.observe(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: id, Parent: s.Root(), State: agent.Idle}})
	}
	return m, s
}

func streamEvent(m *model, id message.ActorID, e agent.Event) {
	m.Update(received{event: conversation.AgentEvent{Agent: id, Event: e}})
}

func TestFocusedStreamStartsOnRootAndSeparatesInterleavedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newModel(ctx, cancel, &fakeSession{}, Options{})
	if m.streamUI.selected != "root" {
		t.Fatal("new sessions must open the root conversation")
	}
	m.resize(124, 34)
	ids := []identity.OutputID{{Agent: "agent-2", Call: 1}, {Agent: "agent-3", Call: 1}}
	for _, id := range ids {
		streamEvent(m, id.Agent, agent.OutputStarted{Output: id})
	}
	for i := range 5 {
		for _, id := range ids {
			streamEvent(m, id.Agent, agent.OutputDelta{Output: id, Text: fmt.Sprintf("%s-piece%d ", id.Agent, i)})
		}
	}
	if strings.Contains(m.viewport.View(), "piece") {
		t.Fatal("a child stream leaked into root's conversation")
	}
	m.selectStream("agent-2")
	view := ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "agent-2-piece4") || strings.Contains(view, "agent-3-piece") {
		t.Fatal(view)
	}
	streamEvent(m, ids[0].Agent, agent.OutputFinished{Output: ids[0], Status: agent.OutputComplete})
	m.observe(conversation.MessageEvent{Message: message.Message{
		ID: "reply", From: "agent-2", To: "root", Kind: message.Reply, Output: &ids[0],
	}})
	m.selectStream("root")
	if !strings.Contains(m.viewport.View(), "agent-2-piece4") || strings.Contains(m.viewport.View(), "agent-3-piece") {
		t.Fatal("a routed child reply must be visible to root exactly once", m.viewport.View())
	}
	m.selectStream("")
	view = ansi.Strip(m.viewport.View())
	if strings.Count(view, "agent-2-piece4") != 1 || strings.Count(view, "agent-3-piece4") != 1 {
		t.Fatal(view)
	}
}

func TestStreamUnreadCountsMessagesNotTokensAndSurvivesHistory(t *testing.T) {
	m, _ := focusedSetup(t)
	id := identity.OutputID{Agent: "agent-2", Call: 1}
	streamEvent(m, id.Agent, agent.OutputStarted{Output: id})
	for range 100 {
		streamEvent(m, id.Agent, agent.OutputDelta{Output: id, Text: "x"})
	}
	if n := len(m.streamUI.views[id.Agent].unread); n != 1 {
		t.Fatalf("100 token deltas counted as %d unread messages", n)
	}
	m.selectStream(id.Agent)
	if len(m.streamUI.views[id.Agent].unread) != 0 {
		t.Fatal("visiting a live stream did not mark it read")
	}
	for i := range 30 {
		m.addAttributed("Message", string(id.Agent), fmt.Sprintf("history %d", i), true, id.Agent)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	position := m.streamPosition()
	m.selectStream("root")
	streamEvent(m, id.Agent, agent.OutputDelta{Output: id, Text: "more"})
	m.selectStream(id.Agent)
	if m.viewport.AtBottom() || m.streamPosition().anchor != position.anchor || len(m.streamUI.views[id.Agent].unread) != 1 {
		t.Fatal("switching to a saved history position marked unseen output read")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if len(m.streamUI.views[id.Agent].unread) != 0 {
		t.Fatal("returning to latest did not clear unread activity")
	}
}

func TestStreamScrollAnchorSurvivesEarlierGrowthAndOtherAgentEvents(t *testing.T) {
	m, _ := focusedSetup(t)
	m.selectStream("")
	id := identity.OutputID{Agent: "agent-2", Call: 1}
	streamEvent(m, id.Agent, agent.OutputStarted{Output: id})
	streamEvent(m, id.Agent, agent.OutputDelta{Output: id, Text: "early reply"})
	for i := range 30 {
		m.addAttributed("Message", "agent-3", fmt.Sprintf("stable paragraph %d", i), true, "agent-3")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	before := m.viewport.View()
	streamEvent(m, id.Agent, agent.OutputDelta{Output: id, Text: strings.Repeat("\nmore lines", 20)})
	if before != m.viewport.View() {
		t.Fatal("growth of an earlier stream displaced the text being read")
	}
	m.selectStream("agent-3")
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	before = m.viewport.View()
	m.addAttributed("Message", "agent-2", "other agent updated", false, "agent-2")
	if before != m.viewport.View() {
		t.Fatal("unrelated activity displaced focused history")
	}
	// Sending to root while watching a child's history must not scroll the child.
	enter(m, "keep working")
	if before != m.viewport.View() {
		t.Fatal("sending to root scrolled the selected child stream")
	}
}

func TestRosterKeyboardPreservesDraftAndAlwaysSendsToRoot(t *testing.T) {
	m, s := focusedSetup(t)
	m.input.SetValue("unfinished draft")
	m.Update(tea.KeyMsg{Type: tea.KeyF6})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.streamUI.focusID != "agent-2" || m.streamUI.selected != "root" || m.input.Value() != "unfinished draft" || m.input.Focused() {
		t.Fatal("agent navigation disturbed the draft or failed to move focus")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(s.sent) != 0 || !m.input.Focused() || m.streamUI.selected != "agent-2" {
		t.Fatal("Enter in the roster should focus the composer, not send")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(s.sent) != 1 || m.pending["1"] != s.Root() {
		t.Fatal("view selection changed the message recipient")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.input.Value() != "    " {
		t.Fatal("Tab must still indent in the composer")
	}
	enter(m, "/focus all")
	if m.streamUI.selected != "" || len(s.sent) != 1 {
		t.Fatal("/focus was sent to a model")
	}
	enter(m, "/focus invalid")
	if m.streamUI.selected != "" || !strings.Contains(m.viewport.View(), "Unknown agent") {
		t.Fatal("invalid agent was silently selected")
	}
}

func TestFocusedLayoutFitsSmallTerminalsAndLargeRosters(t *testing.T) {
	m, _ := focusedSetup(t)
	for i := 4; i < 25; i++ {
		m.observe(conversation.AgentStarted{Agent: conversation.AgentInfo{ID: message.ActorID(fmt.Sprintf("agent-%d", i)), Parent: "root"}})
	}
	m.selectStream("agent-24")
	draft := strings.Repeat("汉字🙂\n    draft\n", 8)
	m.input.SetValue(draft)
	for _, size := range []tea.WindowSizeMsg{{Width: 124, Height: 34}, {Width: 100, Height: 24}, {Width: 100, Height: 12}, {Width: 80, Height: 24}, {Width: 40, Height: 12}, {Width: 15, Height: 8}, {Width: 1, Height: 1}} {
		m.Update(size)
		for _, focused := range []bool{false, true} {
			m.focusRoster(focused)
			view := m.View()
			assertFits(t, view, size.Width, size.Height)
			if size.Width >= 80 && !strings.Contains(view, "agent-24") {
				t.Fatal("selected agent scrolled out of the roster", view)
			}
		}
		if m.input.Value() != draft {
			t.Fatal("responsive layout lost the draft")
		}
	}
}

func TestRosterMouseAndMultilineSelectionStayInTheirPanes(t *testing.T) {
	m, _ := focusedSetup(t)
	x, y := stackLocation(t, m, rosterChoice{id: "agent-2"})
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	if m.streamUI.selected != "agent-2" || m.mouseSelection != nil {
		t.Fatal("roster click started a copy instead of selecting an agent")
	}
	m.addAttributed("Work", "agent-2", "FIRST\nSECOND", false, "agent-2")
	x, y = screenLocation(t, m.View(), "FIRST")
	_, endY := screenLocation(t, m.View(), "SECOND")
	m.copyText = func(text string) error {
		if text != "FIRST\n└ SECOND" {
			t.Errorf("copy included the sidebar: %q", text)
		}
		return nil
	}
	m.Update(mouseAt(tea.MouseActionPress, tea.MouseButtonLeft, x, y))
	m.Update(mouseAt(tea.MouseActionMotion, tea.MouseButtonLeft, x+5, endY))
	frozen := m.View()
	m.addAttributed("Message", "agent-3", "incoming", false, "agent-3")
	if m.View() != frozen {
		t.Fatal("activity disturbed the selected text")
	}
	_, cmd := m.Update(mouseAt(tea.MouseActionRelease, tea.MouseButtonLeft, x+5, endY))
	if cmd == nil {
		t.Fatal("selection did not copy")
	}
	m.Update(cmd())
}

func TestWorkStatusIsDistinctFromAgentLifecycleAndReassignment(t *testing.T) {
	m, _ := focusedSetup(t)
	w := work.Work{ID: "w1", Revision: 1, Owner: "root", Assignee: "agent-2", Kind: work.Implementation, State: work.Active, Task: "Stream routing"}
	m.observe(conversation.WorkEvent{Event: work.Event{Kind: work.WorkAssigned, Work: w}})
	w.Revision, w.State = 2, work.NeedsCheck
	m.observe(conversation.WorkEvent{Event: work.Event{Kind: work.ReviewRequested, Work: w}})
	m.observe(conversation.AgentStateChanged{Agent: "agent-2", State: agent.Idle, Revision: 3})
	if current, ok := m.streamWork("agent-2"); !ok || workStatus(current) != "ready for review" || m.streamState("agent-2") != "idle" {
		t.Fatal("idle was treated as completed work")
	}
	w.Revision, w.State, w.Assignee = 3, work.Active, "agent-3"
	m.observe(conversation.WorkEvent{Event: work.Event{Kind: work.WorkReassigned, Work: w}})
	if _, ok := m.streamWork("agent-2"); ok {
		t.Fatal("reassigned work remained attached to its previous agent")
	}
	stale := w
	stale.Revision, stale.Assignee = 1, "agent-2"
	m.observe(conversation.WorkEvent{Event: work.Event{Work: stale}})
	if current, _ := m.streamWork("agent-3"); current.Revision != 3 {
		t.Fatal("stale work snapshot overwrote the new assignment")
	}
	w.Revision, w.State = 4, work.Accepted
	m.observe(conversation.WorkEvent{Event: work.Event{Work: w}})
	if current, _ := m.streamWork("agent-3"); workStatus(current) != "completed" {
		t.Fatal("accepted work did not appear completed")
	}
}

func TestInspectionAndCopyDoNotConsumeBackgroundUnread(t *testing.T) {
	m, _ := focusedSetup(t)
	m.selectStream("agent-2")
	enter(m, "/transcript agent-2")
	m.observe(conversation.CommentaryEvent{Agent: "agent-2", Content: "new progress"})
	if len(m.streamUI.views["agent-2"].unread) != 1 {
		t.Fatal("inspection marked the hidden live stream read")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	m.observe(conversation.CommentaryEvent{Agent: "agent-2", Content: "more progress"})
	if len(m.streamUI.views["agent-2"].unread) != 1 {
		t.Fatal("copy mode marked hidden activity read")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(m.viewport.View(), "more progress") {
		t.Fatal("resuming lost background output")
	}
}

func TestThinkingStillStaysHiddenInFocusedStream(t *testing.T) {
	m, _ := focusedSetup(t)
	m.selectStream("agent-2")
	id := identity.OutputID{Agent: "agent-2", Call: 1}
	streamEvent(m, id.Agent, agent.OutputStarted{Output: id})
	streamEvent(m, id.Agent, agent.OutputDelta{Output: id, Channel: provider.ChannelReasoning, Text: "hidden thought"})
	if strings.Contains(m.View(), "hidden thought") {
		t.Fatal("focused view exposed reasoning without opting in")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if strings.Contains(m.View(), "hidden thought") || m.outputEntry(id).reasoning != "hidden thought" {
		t.Fatal("output toggle exposed or destroyed recorded reasoning")
	}
}

func TestRosterReportsFailuresAndKeepsNewestContextMeasurement(t *testing.T) {
	m, _ := focusedSetup(t)
	m.observe(conversation.ContextTokensEvent{Agent: "agent-2", Revision: 9, Count: 12402})
	m.finishAgentTableCount(agentTableCount{agent: "agent-2", revision: 8, count: 500})
	if c := m.streamUI.views["agent-2"].context; c.count != 12402 || c.revision != 9 {
		t.Fatal("a late /agents count replaced a newer automatic measurement")
	}
	m.observe(conversation.ContextTokensEvent{Agent: "agent-2", Revision: 10, Error: "unavailable"})
	if c := m.streamUI.views["agent-2"].context; !c.failed || c.label() != "context tokens unavailable" {
		t.Fatal("a failed measurement was displayed as zero")
	}
	m.observe(conversation.AgentExited{Agent: "agent-3", Err: errors.New("provider unavailable")})
	if !m.streamNeedsAttention("agent-3") || !strings.Contains(m.streamSummary(), "1 need attention") {
		t.Fatal("an agent failure was invisible while watching another stream")
	}
}
