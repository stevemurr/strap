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
	m.activityCollapsed[id] = !m.activityCollapsed[id]
	m.renderTranscript(false)
	return nil
}
func activitySelector(id identity.OutputID) string { return fmt.Sprintf("%s/%d", id.Agent, id.Call) }
