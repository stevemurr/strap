package tui

import (
	"strings"
	"testing"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/research"
)

func TestDeepResearchProgressCoalescesAndDoesNotWakeRoot(t *testing.T) {
	m, session := setup(t)
	start := len(m.entries)
	e := research.Event{RunID: "research-1", Binding: research.Binding{Actor: "researcher", WorkID: "work-1"}, Kind: "started", Stage: "plan"}
	for _, stage := range []string{"plan", "scout: model", "read", "verify: model"} {
		e.Stage = stage
		e.Completed++
		m.Update(received{event: conversation.ResearchEvent{Event: e}})
	}
	if len(m.entries) != start+1 || !strings.Contains(m.entries[start].body, "verify: model") || len(session.sent) != 0 {
		t.Fatal("research stages flooded transcript or woke root")
	}
	e.Kind = "source"
	m.Update(received{event: conversation.ResearchEvent{Event: e}})
	if len(m.entries) != start+1 {
		t.Fatal("source added transcript row")
	}
	e.Kind = "finished"
	e.Report = &research.Report{Status: "partial", StopReason: "deadline", Summary: "One verified finding.", Spend: research.Spend{Searches: 3, Fetches: 2, ModelCalls: 7}}
	m.Update(received{event: conversation.ResearchEvent{Event: e}})
	if len(m.entries) != start+1 || !strings.Contains(m.entries[start].body, "partial · deadline") || !strings.Contains(m.entries[start].body, "7 model calls") {
		t.Fatal(m.entries[start].body)
	}
}
