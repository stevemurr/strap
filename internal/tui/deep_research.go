package tui

import (
	"fmt"
	"strings"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/research"
)

// One row follows a research run. Source and usage events update retained state
// without flooding the transcript or waking the root agent.
func (m *model) observeResearch(e research.Event) {
	if e.Kind == "source" || e.Kind == "usage" {
		return
	}
	body := fmt.Sprintf("%s · %d completed steps", strings.ReplaceAll(e.Stage, "_", " "), e.Completed)
	if e.Report != nil && e.Kind == "finished" {
		body = fmt.Sprintf("%s · %s\n\n%s\n\n%d searches · %d reads · %d model calls", e.Report.Status, e.Report.StopReason, e.Report.Summary, e.Report.Spend.Searches, e.Report.Spend.Fetches, e.Report.Spend.ModelCalls)
	}
	for i := range m.entries {
		if m.entries[i].researchRun == e.RunID {
			m.entries[i].body = safeText(body)
			m.entries[i].renderWidth = 0
			if !m.selecting {
				m.renderTranscript(false)
			}
			return
		}
	}
	m.addEntry(entry{researchRun: e.RunID, label: "Deep research", meta: e.Binding.WorkID + " · " + e.RunID, body: safeText(body), actors: []message.ActorID{message.ActorID(e.Binding.Actor)}}, false)
}
