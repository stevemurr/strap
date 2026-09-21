package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// A busy run with a bounded tail of command results in each problem. Build the
// history outside the timer so benchmarks measure interaction, not fixtures.
func renderBenchEval(b *testing.B) *evalModel {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	m := newEvalModel(ctx, cancel, eval.Options{})
	m.width, m.height = 180, 55
	now := time.Now()
	for problem := 0; problem < 4; problem++ {
		task := eval.Task{ID: fmt.Sprintf("medium-%02d-fixture", problem), Title: "Rendering benchmark", Tier: "medium"}
		m.observe(eval.Progress{Task: task, Phase: eval.Running, Root: "root", At: now})
		a := m.problems[problem].activity
		for i := 0; i < 80; i++ {
			a.streamUI.nextEntry++
			actor := message.ActorID("root")
			a.entries = append(a.entries, entry{serial: a.streamUI.nextEntry, label: "Tool", actors: []message.ActorID{actor}, tool: toolKey{agent: actor, call: fmt.Sprint(i)}, toolInfo: displayTool(agent.ToolActivity{Call: provider.ToolCall{Name: "shell", Arguments: []byte(`{"input":{"command":"go test ./internal/tui -run TestRendering"}}`)}, StartedAt: now, FinishedAt: now, Result: tool.Text(strings.Repeat("ok github.com/stevemurr/strap/internal/tui 0.024s\n", 160))})})
		}
		id := identity.OutputID{Agent: "root", Call: 1}
		a.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputStarted{Output: id}})
		a.renderTranscript(true)
	}
	m.selected = 0
	m.followActive = false
	return m
}

func BenchmarkEvalProblemSwitch(b *testing.B) {
	m := renderBenchEval(b)
	b.ReportAllocs()
	frameBytes := len(m.View())
	b.ResetTimer()
	b.ReportMetric(float64(frameBytes), "bytes/frame")
	for i := 0; i < b.N; i++ {
		key := tea.KeyDown
		if i%2 == 1 {
			key = tea.KeyUp
		}
		m.Update(tea.KeyMsg{Type: key})
		_ = m.View()
	}
}
func BenchmarkEvalSpinner(b *testing.B) {
	m := renderBenchEval(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Update(spinner.TickMsg{ID: m.spinner.ID()})
		_ = m.View()
	}
}
func BenchmarkEvalReasoningDelta(b *testing.B) {
	m := renderBenchEval(b)
	p := m.problems[1]
	delta := eval.Progress{Task: p.task, Event: conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: identity.OutputID{Agent: "root", Call: 1}, Channel: provider.ChannelReasoning, Text: "next thought "}}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.observe(delta)
		_ = m.View()
	}
}

func BenchmarkEvalTranscriptRebuild(b *testing.B) {
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			m := renderBenchEval(b)
			a := m.current().activity
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					for j := range a.entries {
						a.entries[j].toolLayout = nil
					}
				}
				a.renderTranscript(false)
			}
		})
	}
}
