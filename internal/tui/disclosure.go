package tui

import (
	"fmt"
	"github.com/stevemurr/strap/identity"
	"strconv"
	"strings"
)

// Disclosure state is presentation-only and keyed by stable response identity.
// Contiguous segments render in place even when agents interleave.
func (m *model) toggleActivity(selector string) error {
	parts := strings.Split(selector, "/")
	if len(parts) != 2 {
		return fmt.Errorf("use /activity agent-id/response-number")
	}
	call, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || call == 0 {
		return fmt.Errorf("invalid response number")
	}
	id := identity.OutputID{Agent: identity.ActorID(parts[0]), Call: call}
	if m.outputEntry(id) == nil {
		return fmt.Errorf("response not found")
	}
	if m.activityCollapsed == nil {
		m.activityCollapsed = map[identity.OutputID]bool{}
	}
	if m.folds.expanded == nil {
		m.folds.expanded = map[foldKey]bool{}
	}
	var tools []*entry
	for i := range m.entries {
		e := &m.entries[i]
		if e.toolInfo != nil && e.activityOutput != nil && *e.activityOutput == id {
			tools = append(tools, e)
		}
	}
	expand := true
	if len(tools) > 0 {
		expand = !m.toolExpanded(tools[0])
	}
	m.activityCollapsed[id] = !expand
	for _, e := range tools {
		m.folds.expanded[foldKey{serial: e.serial, tool: true}] = expand
	}

	m.renderTranscript(false)
	return nil
}
