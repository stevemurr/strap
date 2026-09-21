package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func TestMultilineCommandDoesNotPadShortSourceLines(t *testing.T) {
	withTerminalTheme(t, false, termenv.TrueColor)
	m, _ := setup(t)
	m.resize(80, 50)
	command := "cat > audit.go <<'EOF'\npackage main\n\n// " + strings.Repeat("long comment ", 12) + "\nfunc main() {\n    println(\"hello\")\n}\nEOF"
	args, _ := json.Marshal(map[string]any{"input": map[string]string{"command": command}})
	e := entry{toolInfo: displayTool(agent.ToolActivity{Call: provider.ToolCall{Name: "shell", Arguments: args}, FinishedAt: time.Now()})}
	view := ansi.Strip(m.renderTool(&e, 0))
	blank := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.Trim(strings.TrimSpace(line), "│") == "" {
			blank++
		}
	}
	if blank != 1 {
		t.Fatalf("got %d blank source lines, want the one authored blank line:\n%s", blank, view)
	}
}

func TestPresentationSharedByStrapAndEval(t *testing.T) {
	for _, host := range []string{"strap", "strap-eval"} {
		for _, dark := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dark=%v", host, dark), func(t *testing.T) {
				withTerminalTheme(t, dark, termenv.TrueColor)
				var m *model
				var emit func(conversation.Event)
				var update func(tea.Msg)
				var view func() string
				if host == "strap" {
					m, _ = setup(t)
					emit = func(event conversation.Event) { m.Update(received{event: event}) }
					update = func(msg tea.Msg) { m.Update(msg) }
					view = m.View
				} else {
					e, task := evalSetup(t)
					m = e.current().activity
					emit = func(event conversation.Event) { e.Update(eval.Progress{Task: task, Event: event}) }
					update = func(msg tea.Msg) { e.Update(msg) }
					view = e.View
				}
				m.entries = nil
				update(tea.WindowSizeMsg{Width: 120, Height: 55})
				m.input.SetValue("keep my draft")
				a := agent.ToolActivity{Call: provider.ToolCall{ID: "read", Name: "read_file", Arguments: []byte(`{"input":{"path":"src/main.go"}}`)}, StartedAt: time.Now()}
				emit(conversation.ToolEvent{Agent: "root", Activity: a})
				a.FinishedAt = time.Now()
				a.Result, _ = tool.JSON(tool.ReadFileResult{Path: "src/main.go", Content: "1\tpackage main\n2\tfunc main() { println(\"hello\") }\n"})
				emit(conversation.ToolEvent{Agent: "root", Activity: a})
				a = agent.ToolActivity{Call: provider.ToolCall{ID: "shell", Name: "shell", Arguments: []byte(`{"input":{"command":"go test -run 'TestMain' -count=1 ./..."}}`)}, StartedAt: time.Now()}
				emit(conversation.ToolEvent{Agent: "root", Activity: a})
				a.FinishedAt, a.Result = time.Now(), tool.Text("ok   example/main   0.004s")
				emit(conversation.ToolEvent{Agent: "root", Activity: a})
				emit(conversation.WorkEvent{Event: work.Event{Kind: work.WorkAssigned, Work: work.Work{ID: "audit", Task: "Audit the implementation", Owner: "root", Assignee: "root", State: work.Active}}})
				emit(conversation.WorkEvent{Event: work.Event{Kind: work.WorkProgressReported, Work: work.Work{Assignee: "root"}, Change: &work.Change{ProgressReports: []work.WorkProgressReport{{ID: "audit-report", Position: &work.WorkPosition{Activity: "Independent tests pass.", NextStep: "Verify remaining edge cases.", Uncertainty: "None identified."}}}}}})
				p := planFixture()
				emit(conversation.WorkEvent{Event: work.Event{Kind: work.PlanChanged, Plan: &p}})
				plain := ansi.Strip(view())
				for _, want := range []string{"Read src/main.go", "Ran go test -run 'TestMain' -count=1 ./...", "◆ Work", "↳ Progress", "Next: Verify remaining edge cases.", "2/5 complete"} {
					if !strings.Contains(plain, want) {
						t.Fatalf("missing %q:\n%s", want, plain)
					}
				}
				assertOutline := func() {
					t.Helper()
					for _, step := range p.Steps {
						if strings.Count(ansi.Strip(view()), step.Title) != 1 {
							t.Fatalf("flat outline does not show %q exactly once:\n%s", step.Title, ansi.Strip(view()))
						}
					}
				}
				assertOutline()
				if dir := os.Getenv("STRAP_TUI_PRESENTATION_CAPTURE"); dir != "" {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-dark-%v.ansi", host, dark)), []byte(view()), 0600); err != nil {
						t.Fatal(err)
					}
				}
				m.currentPlan().selected = "verify"
				update(tea.KeyMsg{Type: tea.KeyCtrlP})
				collapsed := dockText(m)
				if !strings.Contains(collapsed, p.Steps[2].Title) || strings.Contains(collapsed, p.Steps[4].Title) || strings.Count(collapsed, "\n") != 1 {
					t.Fatalf("collapsed plan should be a rule plus current work row:\n%s", collapsed)
				}
				update(tea.KeyMsg{Type: tea.KeyCtrlP})
				assertOutline()
				if m.input.Value() != "keep my draft" {
					t.Fatal("presentation controls changed the draft")
				}
			})
		}
	}
}

func TestSyntaxPreservesShellTextAndTerminalProfiles(t *testing.T) {
	for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.Ascii} {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			withTerminalTheme(t, false, profile)
			for _, command := range []string{
				"go test -run 'TestMain' -count=1 ./... 2>&1",
				"cat > file.go <<'EOF'\npackage main\n\nfunc main() {}\nEOF\necho done\n",
				"cat <<EOF > 'data.json'\n{\"key\": 42}\nEOF",
				"cat > x.go <<-EOF\n\tpackage main\n\tEOF",
				"cat > x.go <<EOF\npackage main\n", // incomplete input
				"echo '界\n\nsecond line'\n",
			} {
				got := shellText(command)
				if ansi.Strip(got) != strings.ReplaceAll(command, "\t", "    ") {
					t.Fatalf("highlighting changed shell source:\nwant %q\n got %q", command, ansi.Strip(got))
				}
				if profile == termenv.Ascii && strings.Contains(got, "\x1b") {
					t.Fatal("NO_COLOR retained syntax escape codes")
				}
			}
		})
	}
}

func TestToolStylesAndCacheFollowTerminalTheme(t *testing.T) {
	withTerminalTheme(t, false, termenv.TrueColor)
	m, _ := setup(t)
	m.resize(120, 40)
	e := entry{toolInfo: displayTool(agent.ToolActivity{Call: provider.ToolCall{Name: "shell", Arguments: []byte(`{"input":{"command":"go test -run 'Example' ./..."}}`)}, FinishedAt: time.Now()})}
	light := m.renderTool(&e, 0)
	if !strings.Contains(light, lipgloss.NewStyle().Bold(true).Render("Ran")) || !strings.Contains(light, keywordStyle.Render("-run")) || !strings.Contains(light, successStyle.Render("'Example'")) {
		t.Fatal("tool label, flags, or strings lost their distinct styling")
	}
	lipgloss.SetHasDarkBackground(true)
	dark := m.renderTool(&e, 0)
	if light == dark || ansi.Strip(light) != ansi.Strip(dark) {
		t.Fatal("tool theme cache retained old colors or changed content")
	}
	lipgloss.SetColorProfile(termenv.Ascii)
	if plain := m.renderTool(&e, 0); strings.Contains(plain, "\x1b") || plain != ansi.Strip(light) {
		t.Fatalf("NO_COLOR retained cached colors or changed content: got %q want %q", plain, ansi.Strip(light))
	}
}

func TestCollapsedPlanPrioritizesCurrentTitleAtNarrowWidths(t *testing.T) {
	m, _ := setup(t)
	m.rememberPlan(planFixture())
	m.currentPlan().expanded = false
	view := ansi.Strip(strings.Join(planText(m.planLines(40, 14)), "\n"))
	if !strings.Contains(view, "Add search") || !strings.Contains(view, "2/5") || strings.Contains(view, "In progress") {
		t.Fatalf("collapsed title crowded out by status chrome:\n%s", view)
	}
}

func TestReadFileSourceHighlightingPreservesNumberedLines(t *testing.T) {
	withTerminalTheme(t, false, termenv.TrueColor)
	result, _ := tool.JSON(tool.ReadFileResult{Path: "main.go", Content: "7\tfunc main() {\n8\t\tprintln(\"hello\")\n9\t}\n"})
	e := entry{toolInfo: displayTool(agent.ToolActivity{Call: provider.ToolCall{Name: "read_file", Arguments: []byte(`{"input":{"path":"main.go"}}`)}, FinishedAt: time.Now(), Result: result})}
	rows := e.toolResultRows(70)
	view := strings.Join(rows, "\n")
	if len(rows) != 3 || !strings.Contains(ansi.Strip(view), `println("hello")`) {
		t.Fatalf("source lines were changed: %q", view)
	}
	if !strings.Contains(view, "\x1b[") {
		t.Fatal("read_file source has no syntax highlighting")
	}
	for _, number := range []string{"7", "8", "9"} {
		if !strings.Contains(ansi.Strip(view), number) {
			t.Fatal("lost source line numbers")
		}
	}
}
