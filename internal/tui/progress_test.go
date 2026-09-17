package tui

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
	"strings"
	"testing"
	"time"
)

func TestResearchProgressShowsAttributedFindingsAndDistinctDelivery(t *testing.T) {
	m, s := setup(t)
	m.resize(140, 80)
	m.entries = nil
	e := work.Event{Kind: work.WorkProgressReported, Work: work.Work{ID: "w", Kind: work.Research, State: work.Active, Owner: "root", Assignee: "worker"}, Change: &work.Change{ProgressReports: []work.WorkProgressReport{{ID: "r", WorkID: "w", Position: &work.WorkPosition{Objective: "inspect", Uncertainty: "unverified"}, Findings: []work.ProgressFinding{{ID: "f", Basis: work.Inferred, Claim: "may fail", Limitation: "not tested"}}}}}}
	m.observe(conversation.WorkEvent{Event: e})
	all := ansi.Strip(m.viewport.View())
	if !strings.Contains(all, "inferred · f: may fail") || !strings.Contains(all, "Limitation: not tested") || !strings.Contains(all, "Uncertainty: unverified") {
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
