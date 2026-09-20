package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func shellTranscriptFixture(m *model) identity.OutputID {
	m.observe(conversation.MessageEvent{Message: message.Message{ID: "prompt", From: message.User, To: m.session.Root(), Kind: message.Instruction, Content: "Check the tests"}})
	id := identity.OutputID{Agent: m.session.Root(), Call: 1}
	m.observe(conversation.AgentEvent{Agent: id.Agent, Event: agent.OutputStarted{Output: id}})
	m.observe(conversation.AgentEvent{Agent: id.Agent, Event: agent.OutputDelta{Output: id, Channel: provider.ChannelReasoning, Text: "private thought"}})
	progress(m, id.Agent, "Checking cancellation now.")
	a := agent.ToolActivity{Call: provider.ToolCall{ID: "test", Name: "shell", Arguments: []byte(`{"input":{"command":"go test ./internal/tui"}}`)}, StartedAt: time.Now()}
	m.observe(conversation.ToolEvent{Agent: id.Agent, Activity: a})
	a.FinishedAt = time.Now()
	code := 1
	a.Result, _ = tool.JSON(tool.ShellResult{Started: true, ExitCode: &code, Output: "first line\nsecond line\nlast output line\n", Truncated: true})
	m.observe(conversation.ToolEvent{Agent: id.Agent, Activity: a})
	progress(m, id.Agent, "One check failed; investigating.")
	return id
}

func checkShellTranscript(t *testing.T, m *model, open bool) {
	t.Helper()
	view := ansi.Strip(m.viewport.View())
	previous := -1
	for _, part := range []string{"› Check the tests", "Checking cancellation now.", "Ran go test ./internal/tui", "One check failed; investigating."} {
		index := strings.Index(view, part)
		if index <= previous {
			t.Fatalf("missing or reordered %q:\n%s", part, view)
		}
		previous = index
	}
	for _, part := range []string{"You", "Strap", "Thinking", "private thought", `"output":`, `"exit_code":`} {
		if strings.Contains(view, part) {
			t.Fatalf("unexpected %q:\n%s", part, view)
		}
	}
	for _, part := range []string{agentGlyph(m.session.Root()), "exit 1", "Output truncated by shell"} {
		if !strings.Contains(view, part) {
			t.Fatalf("missing %q:\n%s", part, view)
		}
	}
	if strings.Contains(view, "last output line") != open {
		t.Fatal("incorrect output disclosure", view)
	}
}

func TestShellTranscriptAndEvalShareOutputDisclosure(t *testing.T) {
	for _, inEval := range []bool{false, true} {
		name := "strap"
		if inEval {
			name = "strap-eval"
		}
		t.Run(name, func(t *testing.T) {
			var m *model
			var update func(tea.Msg)
			if inEval {
				e, task := evalSetup(t)
				e.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
				m = e.current().activity
				update = func(msg tea.Msg) { e.Update(msg) }
				// Exercise the same observation path used by the evaluation runner.
				e.observe(eval.Progress{Task: task, Event: conversation.AgentStateChanged{Agent: "root", State: agent.Running}})
			} else {
				m, _ = setup(t)
				m.entries = nil
				m.resize(140, 60)
				update = func(msg tea.Msg) { m.Update(msg) }
			}
			id := shellTranscriptFixture(m)
			m.input.SetValue("unsent draft")
			checkShellTranscript(t, m, false)
			update(tea.KeyMsg{Type: tea.KeyCtrlT})
			checkShellTranscript(t, m, true)
			update(tea.KeyMsg{Type: tea.KeyCtrlT})
			checkShellTranscript(t, m, false)
			if m.outputEntry(id).reasoning != "private thought" || m.input.Value() != "unsent draft" {
				t.Fatal("view changed retained data or draft")
			}
		})
	}
}

func TestAgentIconHoverIsStaticBoundedAndReadOnly(t *testing.T) {
	m, s := setup(t)
	m.entries = nil
	m.resize(100, 30)
	completedToolForTest(m, "root", "read", "README.md")
	m.input.SetValue("draft")
	target := m.badges.targets[0]
	event := tea.MouseMsg{X: 1 + target.column, Y: m.transcriptTop() + target.row, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone}
	m.Update(event)
	peek := m.badges.peek
	if peek == nil || !strings.Contains(ansi.Strip(peek.text), "root") {
		t.Fatal("missing identity preview")
	}
	if peek.x < 0 || peek.x+lipgloss.Width(peek.text) > m.width || peek.y+lipgloss.Height(peek.text) > m.composerTop() {
		t.Fatal("preview covers composer or leaves screen")
	}
	m.Update(event)
	if m.badges.peek.text != peek.text || m.input.Value() != "draft" || len(s.sent) > 0 || s.managed != "" {
		t.Fatal("hover changed state or animated")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.badges.peek != nil || m.input.Value() != "draft" {
		t.Fatal("escape did not dismiss preview")
	}
	// The evaluator translates its activity viewport coordinates too.
	e, _ := evalSetup(t)
	a := e.current().activity
	completedToolForTest(a, "root", "read", "README.md")
	target = a.badges.targets[0]
	e.Update(tea.MouseMsg{X: e.listWidth() + 4 + target.column, Y: 15 + target.row, Action: tea.MouseActionMotion, Button: tea.MouseButtonNone})
	if a.badges.peek == nil || e.ctx.Err() != nil {
		t.Fatal("eval hover missing or changed execution")
	}
}

func TestOutputNavigationVisitsEachCommandOnce(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	m.resize(140, 60)
	completedToolForTest(m, "root", "a", "a.go")
	completedToolForTest(m, "root", "b", "b.go")
	m.toggleToolOutput()
	m.foldKey("f7")
	m.foldKey("home")
	first := m.folds.selected
	m.foldKey("down")
	if m.folds.selected == first {
		t.Fatal("keyboard stopped at duplicate disclosure hint")
	}
	m.foldKey("up")
	if m.folds.selected != first {
		t.Fatal("keyboard skipped first command")
	}
}

func TestNativeToolOutputPreservesMalformedResultsAndCommandNewlines(t *testing.T) {
	a := agent.ToolActivity{Call: provider.ToolCall{Name: "shell", Arguments: []byte(`{"input":{"command":"echo first\n# comment\necho second"}}`)}, Result: tool.Text(`{"started":true,"output":42}`)}
	d := displayTool(a)
	if d.preview != "echo first\n# comment\necho second" || d.result != a.Result.Content.Text() {
		t.Fatal("changed command semantics or discarded malformed output", d)
	}
	a.Call.Name = "custom_tool"
	a.Result = tool.Text(`{"started":true,"output":"custom envelope"}`)
	if displayTool(a).result != a.Result.Content.Text() {
		t.Fatal("decoded an unrelated tool result")
	}
	a.Call.Name = "read_file"
	a.Result, _ = tool.JSON(tool.ReadFileResult{Content: "1\tpackage main\n", More: true})
	d = displayTool(a)
	if !strings.Contains(d.result, "package main") || strings.Contains(d.result, `"content"`) || d.notice == "" {
		t.Fatal("file preview lost content or partial notice", d)
	}
}
