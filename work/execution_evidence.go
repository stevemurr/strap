package work

import "github.com/stevemurr/strap/identity"

type ExecutionEvidence struct {
	WorkID             ID
	AssignedAtRevision Revision
	Actor              identity.ActorID
}
type EvidenceLookup func(string) (ExecutionEvidence, error)

func WithEvidenceLookup(f EvidenceLookup) Option { return func(s *Store) { s.evidenceLookup = f } }
