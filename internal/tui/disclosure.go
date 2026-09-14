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
	var groups [][]*entry
	// Include offscreen streams too. A response can belong to a fold spanning
	// several model calls, or several folds separated by another agent.
	for i := 0; i < len(m.entries); i++ {
		e := &m.entries[i]
		actor := activityActor(e)
		if actor == "" {
			continue
		}
		group := []*entry{e}
		for i+1 < len(m.entries) && activityActor(&m.entries[i+1]) == actor {
			i++
			group = append(group, &m.entries[i])
		}
		for _, row := range group {
			if (row.output != nil && *row.output == id) || (row.activityOutput != nil && *row.activityOutput == id) {
				groups = append(groups, group)
				break
			}
		}
	}
	expand := true
	if len(groups) > 0 {
		expand = !m.foldExpanded(groups[0])
	} else if collapsed, ok := m.activityCollapsed[id]; ok {
		expand = collapsed
	}
	m.activityCollapsed[id] = !expand
	for _, group := range groups {
		m.folds.expanded[foldKey{serial: group[0].serial}] = expand
	}
	m.renderTranscript(false)
	return nil
}
func activitySelector(id identity.OutputID) string { return fmt.Sprintf("%s/%d", id.Agent, id.Call) }
