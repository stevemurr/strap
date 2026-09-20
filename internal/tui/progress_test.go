package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

func TestProgressMarkdownInConversationAndEval(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.Ascii} {
			t.Run(fmt.Sprintf("dark=%t/profile=%v", dark, profile), func(t *testing.T) {
				withTerminalTheme(t, dark, profile)
				m, _ := setup(t)
				m.resize(140, 100)
				m.entries = nil
				e, task := evalSetup(t)
				e.Update(tea.WindowSizeMsg{Width: 160, Height: 100})
				report := work.Event{Kind: work.WorkProgressReported,
					Work: work.Work{ID: "work-tags", Owner: "root", Assignee: "worker", State: work.Active},
					Change: &work.Change{ProgressReports: []work.WorkProgressReport{{
						ID: "report-tags", WorkID: "work-tags", WorkRevision: 2, AssignedAtRevision: 1,
						Position: &work.WorkPosition{
							Objective: "Group **equivalent tags**.",
							Activity:  "Writing the implementation.",
							Note:      "- Sort the bytes\n- Group matching keys\n\nKeep *output deterministic*.",
							NextStep:  "Run `go vet ./...`.",
						},
						Findings: []work.ProgressFinding{{ID: "finding-tags", Basis: work.Inferred,
							Claim:      "## Approach\n\nUse a **canonical key**.\n\n```go\nreturn groups\n```",
							Limitation: "Design only; **not verified**.",
						}},
					}}},
				}
				m.observe(conversation.WorkEvent{Event: report})
				e.Update(eval.Progress{Task: task, Event: conversation.WorkEvent{Event: report}})
				for host, view := range map[string]string{"conversation": m.View(), "eval": e.View()} {
					plain := ansi.Strip(view)
					for _, want := range []string{"worker", "Report details", "Writing the implementation.", "• Sort the bytes", "• Group matching keys", "Keep output deterministic.", "Next:", "go vet ./...", "inferred:", "▎ Approach", "return groups", "Limitation:", "Design only; not verified."} {
						if !strings.Contains(plain, want) {
							t.Fatalf("%s missing %q:\n%s", host, want, plain)
						}
					}
					for _, raw := range []string{"**", "## Approach", "```go", "*output deterministic*"} {
						if strings.Contains(plain, raw) {
							t.Fatalf("%s contains unrendered Markdown %q", host, raw)
						}
					}
					for _, line := range strings.Split(plain, "\n") {
						if strings.Contains(line, "Objective:") && strings.Contains(line, "Activity:") {
							t.Fatal("report fields merged onto one line", line)
						}
					}
				}
				// Resizing reflows the formatted report within each host's bounds.
				for _, width := range []int{80, 40, 20} {
					m.Update(tea.WindowSizeMsg{Width: width, Height: 35})
					e.Update(tea.WindowSizeMsg{Width: width, Height: 35})
					for _, view := range []string{m.View(), e.View()} {
						for _, line := range strings.Split(view, "\n") {
							if ansi.StringWidth(line) > width {
								t.Fatalf("report overflow at width %d", width)
							}
						}
					}
				}
			})
		}
	}
}

func TestResearchDeliveryRendersMarkdown(t *testing.T) {
	m, _ := setup(t)
	m.resize(120, 60)
	m.entries = nil
	m.observe(conversation.WorkEvent{Event: work.Event{
		Kind: work.ResearchDelivered,
		Work: work.Work{ID: "research", Owner: "root", Assignee: "worker", State: work.Delivered},
		Change: &work.Change{ResearchBriefs: []work.ResearchBrief{{
			ID: "brief", Summary: "## Findings\n\nThe behavior is **documented**.",
			Recommendation: "Use the **supported API**.",
			OpenQuestions:  []string{"- Check compatibility\n- Confirm performance"},
		}}},
	}})
	view := ansi.Strip(m.viewport.View())
	for _, want := range []string{"Research delivered · brief", "▎ Findings", "The behavior is documented.", "Recommendation:", "Use the supported API.", "• Check compatibility", "• Confirm performance"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
}

func TestProgressSingleLineMarkdownBlocks(t *testing.T) {
	withTerminalTheme(t, false, termenv.TrueColor)
	for _, tc := range []struct{ name, input, want string }{
		{"heading", "## Verification", "▎ Verification"},
		{"list", "- Check keyboard navigation", "• Check keyboard navigation"},
		{"task", "- [x] Keyboard checks pass", "[✓] Keyboard checks pass"},
		{"quote", "> Needs independent review", "│ Needs independent review"},
		{"code", "    return groups", "┌─ code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := setup(t)
			e := &entry{label: "Progress", body: progressBody(work.Event{Change: &work.Change{ProgressReports: []work.WorkProgressReport{{Position: &work.WorkPosition{Note: tc.input}}}}})}
			got := ansi.Strip(m.renderBodyWidth(e, 80))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("missing rendered block %q:\n%s", tc.want, got)
			}
		})
	}
}

func TestResearchProgressShowsAttributedFindingsAndDistinctDelivery(t *testing.T) {
	m, s := setup(t)
	m.resize(140, 80)
	m.entries = nil
	e := work.Event{Kind: work.WorkProgressReported, Work: work.Work{ID: "w", Kind: work.Research, State: work.Active, Owner: "root", Assignee: "worker"}, Change: &work.Change{ProgressReports: []work.WorkProgressReport{{ID: "r", WorkID: "w", Position: &work.WorkPosition{Objective: "inspect", Uncertainty: "unverified"}, Findings: []work.ProgressFinding{{ID: "f", Basis: work.Inferred, Claim: "may fail", Limitation: "not tested"}}}}}}
	m.observe(conversation.WorkEvent{Event: e})
	all := ansi.Strip(m.viewport.View())
	if !strings.Contains(all, "inferred:") || !strings.Contains(all, "may fail") || !strings.Contains(all, "Limitation:") || !strings.Contains(all, "not tested") || !strings.Contains(all, "Uncertainty:") || !strings.Contains(all, "unverified") {
		t.Fatal(all)
	}
	m.selectStream("root")
	if strings.Contains(m.viewport.View(), "may fail") {
		t.Fatal("activity flooded root stream")
	}
	if workStatus(e.Work) != "researching" {
		t.Fatal(workStatus(e.Work))
	}
	e.Work.State = work.Delivered
	if workStatus(e.Work) != "delivered" {
		t.Fatal(workStatus(e.Work))
	}
	if len(s.sent) > 0 || s.managed != "" {
		t.Fatal("view changed execution")
	}
}
func TestActivityDisclosurePreservesTextChronologyAndExpansion(t *testing.T) {
	m, s := setup(t)
	m.resize(140, 100)
	m.entries = nil
	id := identity.OutputID{Agent: "root", Call: 1}
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputStarted{Output: id}})
	m.observe(conversation.AgentEvent{Agent: "root", Event: agent.OutputDelta{Output: id, Channel: provider.ChannelContent, Text: "Visible root commentary"}})
	add := func(call string) {
		m.observe(conversation.ToolEvent{Agent: "root", Activity: agent.ToolActivity{InvocationID: call, Call: provider.ToolCall{ID: call, Name: call}, StartedAt: time.Now()}})
	}
	add("first")
	if err := m.toggleActivity("root/1"); err != nil {
		t.Fatal(err)
	}
	if err := m.toggleActivity("root/1"); err != nil {
		t.Fatal(err)
	}
	add("second")
	m.addAttributed("Message", "worker", "Interleaved reply", false, "worker")
	add("third")
	m.addAttributed("Error", "root", "Visible failure", false, "root")
	view := ansi.Strip(m.viewport.View())
	if len(m.folds.targets) != 3 || !strings.Contains(view, "Visible root commentary") || !strings.Contains(view, "Visible failure") {
		t.Fatal(view)
	}
	before := len(m.entries)
	if err := m.toggleActivity("root/1"); err != nil {
		t.Fatal(err)
	}
	view = ansi.Strip(m.viewport.View())
	a, b, c := strings.Index(view, agentGlyph("root")+" First"), strings.Index(view, "Interleaved reply"), strings.Index(view, agentGlyph("root")+" Third")
	if a < 0 || b < a || c < b || len(m.entries) != before {
		t.Fatal(view)
	}
	if len(s.sent) != 0 || s.managed != "" {
		t.Fatal("disclosure invoked runtime")
	}
}
