package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func evalSetup(t *testing.T) (*evalModel, eval.Task) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := newEvalModel(ctx, cancel, eval.Options{Mounts: eval.Mounts{Results: "results"}})
	task := eval.Task{ID: "medium-01-cache", Tier: "medium", Title: "Session cache"}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	m.observe(eval.Progress{Task: task, Phase: eval.Starting, At: time.Now()})
	m.observe(eval.Progress{Task: task, Phase: eval.Running, Root: "root", At: time.Now()})
	return m, task
}

func TestEvalShowsLiveMetricsWithoutComposer(t *testing.T) {
	m, task := evalSetup(t)
	emit := func(e conversation.Event) { m.observe(eval.Progress{Task: task, Event: e, At: time.Now()}) }
	id := identity.OutputID{Agent: "root", Call: 1}
	emit(conversation.AgentEvent{Agent: "root", Event: agent.OutputStarted{Output: id}})
	activity := agent.ToolActivity{InvocationID: "a", Call: provider.ToolCall{ID: "call", Name: "read_file", Arguments: []byte(`{"input":{"path":"cache.go"}}`)}, StartedAt: time.Now()}
	emit(conversation.ToolEvent{Agent: "root", Activity: activity})
	emit(conversation.ToolEvent{Agent: "root", Activity: activity}) // Duplicate start.
	if len(m.current().tools) != 1 || m.current().runningTools() != 1 {
		t.Fatal(m.current().tools)
	}
	activity.FinishedAt = time.Now()
	activity.Result = tool.Text("cache source")
	emit(conversation.ToolEvent{Agent: "root", Activity: activity})
	emit(conversation.ContextTokensEvent{Agent: "root", Revision: 2, Count: 18432})
	in, out := int64(75000), int64(2800)
	emit(conversation.UsageEvent{Agent: "root", Observation: agent.UsageObservation{Usage: &provider.Usage{InputTokens: &in, OutputTokens: &out}}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	view := ansi.Strip(m.View())
	for _, want := range []string{"18,432 context tokens", "Tools: 1 · 0 running", "1 model calls", "75,000 / 2,800", "READ ONLY"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	for _, bad := range []string{"Send a message", "/help for commands", "% of"} {
		if strings.Contains(view, bad) {
			t.Fatal("unexpected", bad, view)
		}
	}
	// Typed commands and paste never reach a composer or session control.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/stop root")})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.current().activity.input.Value() != "" || m.ctx.Err() != nil {
		t.Fatal("read-only navigation mutated the run")
	}
	a := m.current().activity
	if len(a.folds.targets) == 0 {
		t.Fatal("shared activity folds missing")
	}
	for _, target := range append([]foldTarget(nil), a.folds.targets...) {
		a.toggleFold(target.key)
	}
	if !strings.Contains(ansi.Strip(a.viewport.View()), "cache.go") {
		t.Fatal(a.viewport.View())
	}
}

func TestEvalSelectionAndTerminalSizes(t *testing.T) {
	m, task := evalSetup(t)
	other := task
	other.ID = "hard-01-other"
	other.Title = "Other problem"
	m.observe(eval.Progress{Task: other, Phase: eval.Queued})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.current().task.ID != other.ID {
		t.Fatal("selection did not change")
	}
	for _, size := range [][2]int{{140, 40}, {100, 24}, {80, 24}, {40, 15}, {20, 5}, {1, 1}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		assertFits(t, m.View(), size[0], size[1])
	}
}

func TestEvalRejectsStaleContextAndShowsMissingCounts(t *testing.T) {
	m, task := evalSetup(t)
	for _, v := range []conversation.ContextTokensEvent{{Agent: "root", Revision: 3, Count: 9000}, {Agent: "root", Revision: 2, Count: 1000}} {
		m.observe(eval.Progress{Task: task, Event: v})
	}
	if !strings.Contains(ansi.Strip(m.View()), "9,000 context tokens") {
		t.Fatal(m.View())
	}
	m.observe(eval.Progress{Task: task, Event: conversation.ContextTokensEvent{Agent: "root", Revision: 4, Error: "unavailable"}})
	if !strings.Contains(ansi.Strip(m.View()), "context tokens unavailable") {
		t.Fatal(m.View())
	}
}

func TestEvalProgramExitsWithoutUserInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	_, err := RunEval(ctx, eval.Options{Problem: "missing", Mounts: eval.Mounts{Problems: t.TempDir(), Results: t.TempDir(), Workspace: t.TempDir(), Outbox: t.TempDir()}}, nil, &output)
	if err == nil || !strings.Contains(err.Error(), "no tasks under") {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("program waited for user input")
	}
}

type evalFinalProvider struct{ block bool }

func (p evalFinalProvider) Submit(ctx context.Context, _ provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.block {
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	}
	return provider.Response{Content: "Finished."}, nil
}

func TestEvalProgramRunsAndCancelsWithoutInput(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "cancel"}[stop], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			dir := t.TempDir()
			ladder, err := filepath.Abs("../../eval/ladder")
			if err != nil {
				t.Fatal(err)
			}
			opts := eval.Options{Config: harness.DefaultConfig(), Deps: harness.Dependencies{Provider: evalFinalProvider{block: stop}}, Mounts: eval.Mounts{Problems: ladder, Results: dir, Workspace: t.TempDir(), Outbox: t.TempDir()}, Quiet: time.Millisecond, Problem: "easy-01-budget-pair"}
			if stop {
				opts.Observe = func(e eval.Progress) {
					if v, ok := e.Event.(conversation.AgentEvent); ok {
						if _, ok := v.Event.(agent.OutputStarted); ok {
							cancel()
						}
					}
				}
			}
			var output bytes.Buffer
			results, err := RunEval(ctx, opts, nil, &output)
			if stop {
				if err == nil || ctx.Err() == nil {
					t.Fatal("expected cancellation", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "easy-01-budget-pair", "result.json")); !os.IsNotExist(err) {
					t.Fatal("interrupted result became durable", err)
				}
			} else if err != nil || len(results) != 1 || results[0].Outcome != eval.Submitted {
				t.Fatal(results, err)
			}
			if _, err := os.Stat(filepath.Join(opts.Mounts.Workspace, "go.mod")); err != nil {
				t.Fatal("mounted workspace was not retained", err)
			}
		})
	}
}

func TestEvalFollowsActiveUntilManuallySelected(t *testing.T) {
	m, first := evalSetup(t)
	second := first
	second.ID = "medium-02-tags"
	m.observe(eval.Progress{Task: second, Phase: eval.Starting, At: time.Now()})
	m.observe(eval.Progress{Task: first, Phase: eval.Finished, Result: &eval.Result{Outcome: eval.Passed, Passed: true}})
	if m.current().task.ID != second.ID {
		t.Fatal("did not follow next active problem")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.observe(eval.Progress{Task: second, Phase: eval.Running, Root: "other"})
	if m.current().task.ID != first.ID {
		t.Fatal("manual selection was overridden")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if m.current().task.ID != second.ID {
		t.Fatal("follow did not resume")
	}
}

func TestEvalMouseExpandsSharedActivity(t *testing.T) {
	m, _ := evalSetup(t)
	a := m.current().activity
	completedToolForTest(a, "root", "call", "cache.go")
	a.viewport.GotoTop()
	target := a.folds.targets[0]
	m.Update(tea.MouseMsg{X: 1 + target.column, Y: m.activityTop() + target.row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !a.folds.expanded[target.key] {
		t.Fatal("mouse missed activity fold")
	}
}

func TestEvalBoundsLongActivityWithoutBreakingUnicode(t *testing.T) {
	m, _ := evalSetup(t)
	a := m.current().activity
	for i := 0; i < 210; i++ {
		a.entries = append(a.entries, entry{serial: uint64(i + 1), label: "Message", body: strings.Repeat("λ ", 2000)})
	}
	trimEvalActivity(a)
	if len(a.entries) > 200 {
		t.Fatal("unbounded entries", len(a.entries))
	}
	bytes := 0
	for _, e := range a.entries {
		bytes += len(e.body) + len(e.reasoning)
	}
	if bytes > 1<<20 {
		t.Fatal("unbounded activity", bytes)
	}
	text := evalDisplayText(strings.Repeat("λ", 40000), 32768)
	if len(text) > 32768 || !utf8.ValidString(text) || !strings.Contains(text, "trace.jsonl") {
		t.Fatal("invalid truncated display")
	}
}

func TestEvalSessionHasNoControlCapabilities(t *testing.T) {
	s := evalSession{root: "root"}
	if _, err := s.Send("root", "hello"); err != errEvalReadOnly {
		t.Fatal(err)
	}
	for _, fn := range []func(identity.ActorID) (conversation.AgentInfo, error){s.PauseAgent, s.ResumeAgent, s.StopAgent} {
		if _, err := fn("root"); err != errEvalReadOnly {
			t.Fatal(err)
		}
	}
}
