package work

import "github.com/stevemurr/strap/identity"

// AdmitResearchDiagnostic serializes validation/capture with publication of
// assignment transitions, then releases every ledger lock before execution.
func (s *Store) AdmitResearchDiagnostic(actor identity.ActorID, id ID) (Work, error) {
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
	return w.Clone(), nil
}

// AdmitExecution binds a worker's command to its sole active assignment, with
// the same serialization as AdmitResearchDiagnostic. It reports false when the
// actor holds no active assignment or several, since a receipt must name one.
func (s *Store) AdmitExecution(actor identity.ActorID) (Work, bool, error) {
	s.emission.Lock()
	defer s.emission.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return Work{}, false, s.failure
	}
	var found Work
	matched := false
	for _, w := range s.works {
		if actor == "" || w.Assignee != actor || w.State != Active || w.AssignedAtRevision == 0 {
			continue
		}
		if matched {
			return Work{}, false, nil
		}
		found, matched = w, true
	}
	if !matched {
		return Work{}, false, nil
	}
	return found.Clone(), true, nil
}
