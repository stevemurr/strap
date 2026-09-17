package tui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func TestHiddenReasoningAndSpinnerKeepTranscriptLayout(t *testing.T) {
	m, _ := setup(t)
	m.entries = nil
	completedToolForTest(m, "root", "read", "README.md")
	id := identity.OutputID{Agent: "root", Call: 1}
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputStarted{Output: id}})
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: id, Text: "Visible answer"}})
	m.working["root"] = true
	before := m.viewport.View()
	anchors := &m.streamUI.lines[0]
	bodyWidth := m.outputEntry(id).renderWidth
	m.Update(received{event: conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: id, Channel: provider.ChannelReasoning, Text: "still recorded"}}})
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	m.Update(received{event: conversation.AgentEvent{Agent: "root", Event: agent.HistoryAppended{Position: 5}}})
	if m.viewport.View() != before || &m.streamUI.lines[0] != anchors || m.outputEntry(id).renderWidth != bodyWidth {
		t.Fatal("invisible update rebuilt the transcript")
	}
	if m.outputEntry(id).reasoning != "still recorded" {
		t.Fatal("reasoning was discarded")
	}
	// Visible changes must continue to invalidate the transcript immediately.
	m.Update(received{event: conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: id, Text: " continues"}}})
	if !strings.Contains(ansi.Strip(m.viewport.View()), "Visible answer continues") {
		t.Fatal("visible streaming stopped")
	}
	e, _ := evalSetup(t)
	a := e.current().activity
	completedToolForTest(a, "root", "read", "README.md")
	anchors = &a.streamUI.lines[0]
	e.Update(spinner.TickMsg{ID: e.spinner.ID()})
	if &a.streamUI.lines[0] != anchors {
		t.Fatal("eval spinner rebuilt transcript")
	}
}

func TestToolLayoutCacheInvalidatesOnCompletionAndResize(t *testing.T) {
	e := entry{toolInfo: displayTool(agent.ToolActivity{Call: provider.ToolCall{Name: "shell"}, StartedAt: time.Now()})}
	if !strings.Contains(strings.Join(e.toolResultRows(40), "\n"), "Waiting for output") {
		t.Fatal("missing running state")
	}
	frozen := e
	result := displayTool(agent.ToolActivity{Call: provider.ToolCall{Name: "shell"}, FinishedAt: time.Now(), Result: tool.Text(strings.Repeat("界", 30))})
	e.toolInfo = result
	rows := e.toolResultRows(40)
	if len(rows) != 2 || strings.Contains(strings.Join(rows, ""), "Waiting") {
		t.Fatal("cached stale result", rows)
	}
	cached := e.toolLayout
	e.toolResultRows(40)
	if e.toolLayout != cached {
		t.Fatal("unchanged result was wrapped again")
	}
	if len(e.toolResultRows(10)) != 6 {
		t.Fatal("resize reused wrong width")
	}
	if !strings.Contains(strings.Join(frozen.toolResultRows(40), ""), "Waiting") {
		t.Fatal("live layout changed frozen snapshot")
	}
}

type frameTestFile struct {
	bytes.Buffer
	writes []string
	limit  int
	fail   error
}

func (f *frameTestFile) WriteString(s string) (int, error) { return f.Write([]byte(s)) }
func (*frameTestFile) Fd() uintptr                         { return ^uintptr(0) }
func (*frameTestFile) Close() error                        { return nil }
func (f *frameTestFile) Write(p []byte) (int, error) {
	f.writes = append(f.writes, string(p))
	if len(f.writes) == 1 && f.limit >= 0 {
		return f.limit, f.fail
	}
	return f.Buffer.Write(p)
}

func TestSynchronizedFrameWriterPreservesPayloadAndTerminalInterface(t *testing.T) {
	f := &frameTestFile{limit: -1}
	w := &frameWriter{File: f}
	var terminal term.File = w
	if terminal.Fd() != f.Fd() {
		t.Fatal("lost terminal descriptor")
	}
	frame := ansi.CursorHomePosition + "first\r\nsecond"
	n, err := w.Write([]byte(frame))
	want := ansi.SetSynchronizedOutputMode + frame + ansi.ResetSynchronizedOutputMode
	if err != nil || n != len(frame) || len(f.writes) != 1 || f.writes[0] != want {
		t.Fatal("frame was not one synchronized write", n, err, f.writes)
	}
	control := ansi.HideCursor
	w.Write([]byte(control))
	if f.writes[1] != control {
		t.Fatal("control sequence was rewritten")
	}
	var redirected bytes.Buffer
	if terminalOutput(&redirected) != &redirected {
		t.Fatal("redirected output was wrapped")
	}
}

func TestSynchronizedFrameWriterResetsAfterShortWrite(t *testing.T) {
	for _, failure := range []error{nil, io.ErrClosedPipe} {
		f := &frameTestFile{limit: len(ansi.SetSynchronizedOutputMode) + 2, fail: failure}
		w := &frameWriter{File: f}
		n, err := w.Write([]byte(ansi.CursorHomePosition + "frame"))
		want := failure
		if want == nil {
			want = io.ErrShortWrite
		}
		if n != 2 || !errors.Is(err, want) || len(f.writes) != 2 || f.writes[1] != ansi.ResetSynchronizedOutputMode {
			t.Fatal("short write left rendering suspended", n, err, f.writes)
		}
	}
}
