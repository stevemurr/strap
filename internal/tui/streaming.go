package tui

import (
	"fmt"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
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
		m.addAttributed(label, fmt.Sprintf("%s · generating", e.Output.Agent), "", false, e.Output.Agent)
		id := e.Output
		row := &m.entries[len(m.entries)-1]
		row.output = &id
		row.reasoningExpanded = m.reasoningExpanded
	case agent.OutputDelta:
		row := m.outputEntry(e.Output)
		if row == nil {
			return
		}
		if e.Channel == provider.ChannelReasoning {
			row.reasoning += safeText(e.Text)
			if !row.contentStarted {
				row.meta = fmt.Sprintf("%s · thinking", e.Output.Agent)
			}
		} else {
			row.contentStarted = true
			row.body += safeText(e.Text)
			row.meta = fmt.Sprintf("%s · responding", e.Output.Agent)
		}
		row.renderWidth = 0
		m.noteStreamEntry(row)
	case agent.OutputFinished:
		row := m.outputEntry(e.Output)
		if row == nil {
			return
		}
		row.meta = fmt.Sprintf("%s · %s", e.Output.Agent, e.Status)
		row.outputFinished = true
		row.outputFailed = e.Status != agent.OutputComplete || e.Err != nil
		row.renderWidth = 0
		m.noteStreamEntry(row)
		if e.Err != nil {
			row.meta += " · " + safeText(e.Err.Error())
		}
	}
	if !m.selecting {
		m.renderTranscript(false)
	}
}

// Ctrl+T is the terminal input for Cmd+T mappings. It changes only view state.
func (m *model) toggleReasoning() {
	m.reasoningExpanded = !m.reasoningExpanded
	for i := range m.entries {
		m.entries[i].reasoningExpanded = m.reasoningExpanded
		m.entries[i].renderWidth = 0
	}
	m.renderTranscript(false)
}
