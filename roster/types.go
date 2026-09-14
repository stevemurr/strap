// Package roster defines application roles and immutable agent registrations.
package roster

import (
	"slices"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

type Role string

const (
	Unknown     Role = "unknown"
	Root        Role = "root"
	Implementor Role = "implementor"
	Auditor     Role = "auditor"
	Researcher  Role = "researcher"
)

func (r Role) Creatable() bool { return r == Implementor || r == Auditor || r == Researcher }
func (r Role) WorkKinds() []work.Kind {
	switch r {
	case Implementor:
		return []work.Kind{work.Implementation, work.Repair}
	case Researcher:
		return []work.Kind{work.Research}
	case Auditor:
		return []work.Kind{work.AuditWork}
	}
	return []work.Kind{}
}
func (r Role) Accepts(k work.Kind) bool {
	return slices.Contains(r.WorkKinds(), k)
}

type CreateRequest struct {
	Role Role `json:"role"`
}
type Registration struct {
	AgentID identity.ActorID `json:"agent_id"`
	Parent  identity.ActorID `json:"parent"`
	Role    Role             `json:"role"`
}
