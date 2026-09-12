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
		m.addDetail(label, fmt.Sprintf("%s · generating", e.Output.Agent), "", false)
		id := e.Output
		row := &m.entries[len(m.entries)-1]
		row.output = &id
		row.reasoningExpanded = true
		if m.reasoningExpanded != nil {
			row.reasoningExpanded = *m.reasoningExpanded
		}
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
			if !row.contentStarted {
				row.contentStarted = true
				if m.reasoningExpanded == nil {
					row.reasoningExpanded = false
				}
			}
			row.body += safeText(e.Text)
			row.meta = fmt.Sprintf("%s · responding", e.Output.Agent)
		}
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

// F3 changes view state only. Further chunks and completion cannot undo it.
func (m *model) toggleReasoning() {
	expanded := true
	if m.reasoningExpanded != nil {
		expanded = !*m.reasoningExpanded
	} else {
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].reasoning != "" {
				expanded = !m.entries[i].reasoningExpanded
				break
			}
		}
	}
	m.reasoningExpanded = &expanded
	for i := range m.entries {
		m.entries[i].reasoningExpanded = expanded
		m.entries[i].renderWidth = 0
	}
	m.renderTranscript(false)
}
