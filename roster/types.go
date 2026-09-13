// Package roster defines application roles and immutable agent registrations.
package roster

import (
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

type Role string

const (
	Unknown     Role = "unknown"
	Root        Role = "root"
	Implementor Role = "implementor"
	Auditor     Role = "auditor"
)

func (r Role) Creatable() bool { return r == Implementor || r == Auditor }
func (r Role) WorkKinds() []work.Kind {
	switch r {
	case Implementor:
		return []work.Kind{work.Implementation, work.Repair}
	case Auditor:
		return []work.Kind{work.AuditWork}
	}
	return []work.Kind{}
}
func (r Role) Accepts(k work.Kind) bool {
	for _, allowed := range r.WorkKinds() {
		if k == allowed {
			return true
		}
	}
	return false
}

type CreateRequest struct {
	Role Role `json:"role"`
}
type Registration struct {
	AgentID identity.ActorID `json:"agent_id"`
	Parent  identity.ActorID `json:"parent"`
	Role    Role             `json:"role"`
}
