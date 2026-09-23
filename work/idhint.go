package work

import (
	"fmt"
	"strings"

	"github.com/stevemurr/strap/identity"
)

// Each ID kind has one reader. A read that receives another kind's ID names that
// reader instead of failing with a bare not-found, because the caller is usually
// holding a real ID in the wrong slot.
var readers = []struct{ prefix, kind, reader string }{
	{"work-", "a work item", "get_work or get_work_progress with work_id"},
	{"report-", "a progress report", "get_work_progress mode report"},
	{"finding-", "a ledger finding", "get_work_progress mode finding"},
	{"brief-", "a delivered research brief", "get_research_brief"},
	{"plan-", "a plan", "get_plan"},
	{"audit-", "an audit", "get_audit"},
	{"run-", "a researcher's deep research run", "get_research_run as that researcher; its delivered result is the research brief"},
}

// Misrouted describes id when its prefix belongs to a kind other than want.
func Misrouted(id, want string) (string, bool) {
	if strings.HasPrefix(id, want) {
		return "", false
	}
	for _, r := range readers {
		if strings.HasPrefix(id, r.prefix) {
			return fmt.Sprintf("%s is %s; read it with %s", id, r.kind, r.reader), true
		}
	}
	return "", false
}

func (s *Store) missingWork(actor identity.ActorID, id ID) error {
	if hint, ok := Misrouted(string(id), "work-"); ok {
		return fmt.Errorf("%w: %s", ErrNotFound, hint)
	}
	return fmt.Errorf("%w: work %s; %s", ErrNotFound, id, s.knownWorks(actor))
}
