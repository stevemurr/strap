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
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func addTool(m *model, actor message.ActorID, name string) {
	m.observe(conversation.ToolEvent{Agent: actor, Activity: agent.ToolActivity{Call: provider.ToolCall{ID: name, Name: name}, StartedAt: time.Now()}})
}

func TestToolRowsRemainSeparateWithAgentAttribution(t *testing.T) {
	m, _ := setup(t)
	m.resize(120, 60)
	m.entries = nil
	for range 3 {
		addTool(m, "agent-2", "read_file")
	}
	addTool(m, "agent-2", "write_file")
	addTool(m, "agent-3", "shell")
	view := ansi.Strip(m.viewport.View())
	if strings.Count(view, agentGlyph("agent-2")) != 4 || strings.Count(view, agentGlyph("agent-3")) != 1 {
		t.Fatal(view)
	}
	expandActivityForTest(m, false)
	tools := 0
	for _, target := range m.folds.targets {
		if target.key.tool {
			tools++
		}
	}
	if tools != 5 {
		t.Fatalf("expanded view lost repeated calls: %d", tools)
	}
	m.add("Strap", "A message between calls", false)
	addTool(m, "agent-2", "read_file")
	expandActivityForTest(m, false)
	tools = 0
	for _, target := range m.folds.targets {
		if target.key.tool {
			tools++
		}
	}
	if tools != 6 {
		t.Fatal("message hid an earlier tool call")
	}
	m.Update(tea.WindowSizeMsg{Width: 25, Height: 24})
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 25 {
			t.Fatal("tool overflow", line)
		}
	}
}

func TestToolCallsDoNotChangeFrozenView(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	addTool(m, "agent-2", "read_file")
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	frozen := m.View()
	addTool(m, "agent-2", "read_file")
	if m.View() != frozen {
		t.Fatal("new call changed frozen display")
	}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if strings.Count(ansi.Strip(m.viewport.View()), agentGlyph("agent-2")) != 1 {
		t.Fatal("resize revealed a new call")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF2})
	if strings.Count(ansi.Strip(m.viewport.View()), agentGlyph("agent-2")) != 2 {
		t.Fatal("resume lost call")
	}
}

func TestMarkdownRendersMessagesAndPreservesSource(t *testing.T) {
	m, _ := setup(t)
	source := "# Summary\n\n**Bold** and *italic* with `code`.\n\n- First\n- Second\n\n```go\nfmt.Println(\"hello\")\n```\n\n| Name | Value |\n| --- | --- |\n| Answer | 42 |\n\n[Docs](https://example.com/docs)"
	for _, label := range []string{"Strap", "Message", "You"} {
		m.add(label, source, false)
		e := &m.entries[len(m.entries)-1]
		rendered := ansi.Strip(m.renderBody(e))
		for _, want := range []string{"Summary", "Bold", "italic", "code", "• First", `fmt.Println("hello")`, "Answer", "42", "https://example.com/docs"} {
			if !strings.Contains(rendered, want) {
				t.Errorf("missing %q: %s", want, rendered)
			}
		}
		for _, raw := range []string{"**Bold**", "*italic*", "```go", "| --- | --- |"} {
			if strings.Contains(rendered, raw) {
				t.Errorf("unrendered Markdown %q", raw)
			}
		}
		if e.body != source {
			t.Fatal("rendering modified source")
		}
	}
	// Diagnostic text should remain literal.
	m.add("Error", "literal **error**", false)
	if !strings.Contains(m.renderBody(&m.entries[len(m.entries)-1]), "**error**") {
		t.Fatal("diagnostic text parsed as Markdown")
	}
}

func TestMarkdownResizeAndTerminalControls(t *testing.T) {
	m, _ := setup(t)
	m.add("Strap", "# Heading\n\n**Safe** &#27;[2J\x1b]52;c;payload\a\n\n```text\n"+strings.Repeat("Long code 界 ", 15)+"\n```\n\n| Column | Second |\n| --- | --- |\n| "+strings.Repeat("word ", 20)+" | value |", false)
	m.input.SetValue("draft")
	for _, width := range []int{100, 40, 12, 1, 80} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		for _, line := range strings.Split(m.View(), "\n") {
			if lipgloss.Width(line) > width {
				t.Errorf("line exceeds %d: %q", width, line)
			}
		}
		rendered := m.renderBody(&m.entries[len(m.entries)-1])
		if strings.Contains(rendered, "\x1b[2J") || strings.Contains(rendered, "\x1b]52") {
			t.Fatal("Markdown emitted untrusted terminal controls")
		}
		if m.input.Value() != "draft" {
			t.Fatal("resize lost draft")
		}
	}
}

func TestMarkdownSanitizationPreservesStyling(t *testing.T) {
	input := "\x1b[1;31mbold red\x1b[0m\x1b[2J\x1b]52;c;payload\a"
	if got := markdownText(input); got != "\x1b[1;31mbold red\x1b[0m" {
		t.Fatalf("unexpected terminal text %q", got)
	}
}
