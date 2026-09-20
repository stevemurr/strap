package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func expandActivityForTest(m *model, results bool) {
	if m.folds.expanded == nil {
		m.folds.expanded = map[foldKey]bool{}
	}
	for _, e := range m.entries {
		if results {
			m.folds.expanded[foldKey{serial: e.serial, tool: true}] = true
		}
	}
	m.renderTranscript(false)
}

func outputForTest(m *model, actor message.ActorID, call uint64, text string) identity.OutputID {
	id := identity.OutputID{Agent: actor, Call: call}
	m.observe(conversation.AgentEvent{Agent: actor, Event: agent.OutputStarted{Output: id}})
	if text != "" {
		m.observe(conversation.AgentEvent{Agent: actor, Event: agent.OutputDelta{Output: id, Text: text}})
	}
	m.observe(conversation.AgentEvent{Agent: actor, Event: agent.OutputFinished{Output: id, Status: agent.OutputComplete}})
	return id
}

func completedToolForTest(m *model, actor message.ActorID, call, path string) {
	start := time.Date(2026, 9, 14, 14, 15, 0, 0, time.UTC)
	a := agent.ToolActivity{Call: provider.ToolCall{ID: call, Name: "read_file", Arguments: []byte(fmt.Sprintf(`{"input":{"path":%q}}`, path))}, StartedAt: start}
	m.observe(conversation.ToolEvent{Agent: actor, Activity: a})
	a.FinishedAt = start.Add(time.Second)
	a.Result = tool.Text("contents of " + path)
	m.observe(conversation.ToolEvent{Agent: actor, Activity: a})
}

func TestToolOnlyOutputsFoldAcrossCallsWithoutLosingHistory(t *testing.T) {
	m, s := setup(t)
	m.entries = nil
	m.resize(140, 60)
	for i := 1; i <= 4; i++ {
		outputForTest(m, "root", uint64(i), "")
		completedToolForTest(m, "root", fmt.Sprint(i), fmt.Sprintf("file-%d.go", i))
	}
	view := ansi.Strip(m.viewport.View())
	if len(m.folds.targets) != 4 || strings.Count(view, "Read file-") != 4 {
		t.Fatal(view)
	}
	if strings.Contains(view, "Activity root/") || strings.Contains(view, "Message  ") || strings.Contains(view, `"path":`) {
		t.Fatal("raw activity leaked", view)
	}
	if len(m.entries) != 8 {
		t.Fatal("folding removed history")
	}
	key := m.folds.targets[0].key
	m.toggleFold(key)
	if len(m.folds.targets) != 4 {
		t.Fatal("expanded fold lost calls", m.folds.targets)
	}
	view = ansi.Strip(m.viewport.View())
	if !strings.Contains(view, `"path": "file-1.go"`) || !strings.Contains(view, "contents of file-1.go") {
		t.Fatal(view)
	}
	if len(s.sent) != 0 || s.managed != "" {
		t.Fatal("disclosure invoked runtime")
	}
}

func TestActivityBoundariesPreserveMessagesAgentsAndFailures(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(140, 80)
	outputForTest(m, "root", 1, "")
	completedToolForTest(m, "root", "a", "first.go")
	outputForTest(m, "worker", 1, "")
	completedToolForTest(m, "worker", "b", "worker.go")
	outputForTest(m, "root", 2, "")
	completedToolForTest(m, "root", "c", "second.go")
	outputForTest(m, "root", 3, "A meaningful progress update")
	completedToolForTest(m, "root", "d", "third.go")
	a := agent.ToolActivity{Call: provider.ToolCall{ID: "failed", Name: "shell", Arguments: []byte(`{"input":{"command":"go test ./..."}}`)}, StartedAt: time.Now()}
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	a.FinishedAt = time.Now()
	a.Err = errors.New("tests failed")
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	view := ansi.Strip(m.viewport.View())
	if len(m.folds.targets) != 5 || !strings.Contains(view, "A meaningful progress update") || strings.Count(view, "tests failed") != 1 {
		t.Fatal(view)
	}
	m.selectStream("root")
	view = ansi.Strip(m.viewport.View())
	if len(m.folds.targets) != 4 || strings.Contains(view, "worker.go") {
		t.Fatal("filter merged across another agent", view)
	}
	for _, want := range []string{"first.go", "second.go", "A meaningful progress update"} {
		if !strings.Contains(view, want) {
			t.Fatal("lost ordered segment", view)
		}
	}
	expandActivityForTest(m, false)
	if !strings.Contains(m.viewport.View(), "third.go") {
		t.Fatal("expanded group lost earlier tool target")
	}
}

func TestFoldCompletionAndResultRemainFrozenUntilResume(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(130, 60)
	a := agent.ToolActivity{Call: provider.ToolCall{ID: "read", Name: "read_file", Arguments: []byte(`{"input":{"path":"notes.md"}}`)}, StartedAt: time.Now()}
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	expandActivityForTest(m, true)
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	a.FinishedAt = time.Now()
	a.Result = tool.Text("NEW RESULT")
	m.observe(conversation.ToolEvent{Agent: "root", Activity: a})
	if m.View() != frozen {
		t.Fatal("completion changed frozen view")
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	if strings.Contains(m.View(), "NEW RESULT") {
		t.Fatal("resize revealed result")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if !strings.Contains(m.View(), "NEW RESULT") || strings.Contains(m.View(), "Waiting for output") {
		t.Fatal(m.View())
	}
}

func TestActivityMouseAndKeyboardKeepDraftAndScroll(t *testing.T) {
	m, s := setup(t)
	m.entries = nil
	m.resize(140, 30)
	for i := 0; i < 8; i++ {
		completedToolForTest(m, "root", fmt.Sprint(i), fmt.Sprintf("file-%d.go", i))
		m.add("Strap", fmt.Sprint("message ", i), false)
	}
	m.viewport.GotoTop()
	m.input.SetValue("unsent draft")
	target := m.folds.targets[0]
	click := tea.MouseMsg{X: 1, Y: m.transcriptTop() + target.row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	m.Update(click)
	if !m.folds.expanded[target.key] || m.viewport.YOffset != 0 || m.input.Value() != "unsent draft" {
		t.Fatal("click lost draft/scroll")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF7})
	if !m.folds.focused || m.input.Focused() {
		t.Fatal("F7 did not focus folds")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.folds.focused || !m.input.Focused() || m.input.Value() != "unsent draft" || len(s.sent) > 0 {
		t.Fatal("fold key sent or lost draft")
	}
}

func TestToolDetailsAreSafeBoundedAndKeepUnicode(t *testing.T) {
	a := agent.ToolActivity{Call: provider.ToolCall{Arguments: []byte(`{"input":{"path":"safe\u001b[2J.md"}}`)}, Result: tool.Text(strings.Repeat("界", 12000) + "\x1b]52;c;secret\a")}
	d := displayTool(a)
	if strings.Contains(d.preview, "\x1b") || strings.Contains(d.result, "\x1b") || !utf8.ValidString(d.result) || !strings.Contains(d.result, "output truncated") || len(d.result) > 33000 {
		t.Fatal("unsafe or unbounded tool display")
	}
}

func TestStoppedToolsDoNotRemainRunningInFolds(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	addTool(m, "worker", "read_file")
	m.observe(conversation.AgentExited{Agent: "worker"})
	if m.entries[0].toolInfo.finished.IsZero() || m.entries[0].toolInfo.failure == "" || len(m.activeTools) > 0 {
		t.Fatal("stopped call still running")
	}
}

func TestQuietComposerMouseSendAndNoFooterNoise(t *testing.T) {
	m, s := setup(t)
	m.resize(140, 40)
	rows := m.renderComposer()
	view := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(view, "Message Strap…") {
		t.Fatal(view)
	}
	for _, noise := range []string{"To Strap", "root", "Idle", "processing", "Ctrl+T", "F6"} {
		if strings.Contains(view, noise) {
			t.Fatal("footer noise", view)
		}
	}
	m.input.SetValue("first\nsecond")
	m.syncCompletion()
	x := 1 + m.viewport.Width - 2
	y := m.composerTop() + m.input.Height() + 1
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if len(s.sent) != 1 || s.sent[0] != "first\nsecond" || m.input.Value() != "" {
		t.Fatal("send action changed routing or draft", s.sent)
	}
	m.input.SetValue("/ag")
	m.syncCompletion()
	y = m.composerTop() + m.input.Height() + 1
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if len(s.sent) != 1 || m.input.Value() != "/agents " {
		t.Fatal("send bypassed command completion")
	}
}

func TestRootHeaderKeepsBackgroundWorkVisibleWithoutSidebar(t *testing.T) {
	m, _ := setup(t)
	m.resize(80, 24)
	m.selectStream("root")
	m.observe(conversation.AgentStateChanged{Agent: "worker", State: agent.Running})
	if !strings.Contains(ansi.Strip(m.composerActivity()), "Working") {
		t.Fatal("background work disappeared", m.View())
	}
	for _, target := range m.stackLayout().targets {
		if target.choice.id == "worker" && strings.Contains(ansi.Strip(m.stackTargetView(target, false)), "●") {
			return
		}
	}
	t.Fatal("working agent is missing from the header", m.View())
}

func TestActivityCommandTogglesTheContainingFold(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	outputForTest(m, "root", 1, "")
	completedToolForTest(m, "root", "a", "a.go")
	outputForTest(m, "root", 2, "")
	completedToolForTest(m, "root", "b", "b.go")
	if err := m.toggleActivity("root/1"); err != nil {
		t.Fatal(err)
	}
	if !m.toolExpanded(&m.entries[1]) || m.toolExpanded(&m.entries[3]) {
		t.Fatal("first response expansion affected another response")
	}
	if err := m.toggleActivity("root/2"); err != nil {
		t.Fatal(err)
	}
	if !m.toolExpanded(&m.entries[1]) || !m.toolExpanded(&m.entries[3]) {
		t.Fatal("second response expansion lost first response state")
	}
}
