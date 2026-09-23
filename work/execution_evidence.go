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
