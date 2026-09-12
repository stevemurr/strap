package tui

import (
	"fmt"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/identity"
)

func (m *model) outputEntry(id identity.OutputID) *entry {
	for i := range m.entries {
		if m.entries[i].output != nil && *m.entries[i].output == id {
			return &m.entries[i]
		}
	}
	return nil
}
func (m *model) observeOutput(fact agent.Event) {
	switch e := fact.(type) {
	case agent.OutputStarted:
		if m.outputEntry(e.Output) != nil {
			return
		}
		label := "Message"
		if e.Output.Agent == m.session.Root() {
			label = "Strap"
		}
		m.addDetail(label, fmt.Sprintf("%s · generating", e.Output.Agent), "", false)
		id := e.Output
		m.entries[len(m.entries)-1].output = &id
	case agent.OutputDelta:
		row := m.outputEntry(e.Output)
		if row == nil {
			return
		}
		row.body += safeText(e.Text)
		row.renderWidth = 0
	case agent.OutputFinished:
		row := m.outputEntry(e.Output)
		if row == nil {
			return
		}
		row.meta = fmt.Sprintf("%s · %s", e.Output.Agent, e.Status)
		row.renderWidth = 0
		if e.Err != nil {
			row.meta += " · " + safeText(e.Err.Error())
		}
	}
	if !m.selecting {
		m.renderTranscript(false)
	}
}
