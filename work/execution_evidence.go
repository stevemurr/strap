package work

import "github.com/stevemurr/strap/identity"

type ExecutionEvidence struct {
	WorkID             ID
	AssignedAtRevision Revision
	Actor              identity.ActorID
}
type EvidenceLookup func(string) (ExecutionEvidence, error)

func WithEvidenceLookup(f EvidenceLookup) Option { return func(s *Store) { s.evidenceLookup = f } }

// NewExecutionRef issues the evidence ref for one command run. It is a short
// store id rather than a long random token: qwen3.6 garbled every 43-character
// ref it retyped from earlier turns (16 rejected reports in
// eval-1790176980218248000), while 7-character ids like work-bnusb0n are copied
// cleanly. Authority comes from the binding check, not from unguessability.
func (s *Store) NewExecutionRef() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mint("execution:")
}

// CanReadExecution reports whether actor may read the runs bound to work id.
// Seeing the work is enough. A run bound to audit work is also readable by
// whoever may read an audit that work recorded: a failing audit's verification
// cites the auditor's runs, and the implementor handed that audit for repair
// could read the audit but not the evidence it cited (medium-20,
// eval-1790176980218248000).
func (s *Store) CanReadExecution(actor identity.ActorID, id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.works[id]
	if !ok {
		return s.missingWork(actor, id)
	}
	if w.visibleTo(actor) {
		return nil
	}
	if w.Kind == AuditWork {
		for _, a := range s.audits {
			if a.WorkID == id && s.canReadAudit(actor, a) {
				return nil
			}
		}
	}
	return ErrForbidden
}
