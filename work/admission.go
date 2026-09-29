package work

import "github.com/stevemurr/strap/identity"

// AdmitResearchRun serializes validation/capture with publication of
// assignment transitions, then releases every ledger lock before execution.
func (s *Store) AdmitResearchRun(actor identity.ActorID, id ID) (Work, error) {
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
	if actor == "" || w.Assignee != actor || !w.Kind.Investigation() {
		return Work{}, ErrForbidden
	}
	if w.State != Active {
		return Work{}, ErrState
	}
	return w.Clone(), nil
}

// Holds reports whether work id is still active under the assignment made at
// revision at.
func (s *Store) Holds(id ID, at Revision) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.works[id]
	return ok && w.State == Active && w.AssignedAtRevision == at
}

// AdmitExecution binds a worker's command to its sole active assignment, with
// the same serialization as AdmitResearchRun. It reports false when the
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
