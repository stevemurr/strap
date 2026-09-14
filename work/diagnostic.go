package work

import "github.com/stevemurr/strap/identity"

// AdmitResearchDiagnostic serializes validation/capture with publication of
// assignment transitions, then releases every ledger lock before execution.
func (s *Store) AdmitResearchDiagnostic(actor identity.ActorID, id ID, binding Revision) (Work, error) {
	s.emission.Lock()
	defer s.emission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return Work{}, s.failure
	}
	w, ok := s.works[id]
	if !ok {
		return Work{}, ErrNotFound
	}
	if actor == "" || w.Assignee != actor || w.Kind != Research {
		return Work{}, ErrForbidden
	}
	if w.State != Active {
		return Work{}, ErrState
	}
	if binding == 0 || binding != w.AssignedAtRevision {
		return Work{}, ErrConflict
	}
	return w.Clone(), nil
}
