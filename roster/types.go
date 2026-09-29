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
	Manager     Role = "manager"
	Debugger    Role = "debugger"
	Implementor Role = "implementor"
	Auditor     Role = "auditor"
	// The web researcher searches and reads the web; the deep researcher runs
	// the deep research engine, which only an explicit request calls for.
	WebResearcher  Role = "web_researcher"
	DeepResearcher Role = "deep_researcher"
	// The experimenter measures how the code behaves, in its own copy of the
	// workspace.
	Experimenter Role = "experimenter"
	// The reviewer reads the workspace, which the manager cannot.
	Reviewer Role = "reviewer"
	// Agent is a single-agent session's one agent: the user talks to it, and
	// it reads, changes, runs and researches everything itself.
	Agent Role = "agent"
)

// Creatable reports whether an agent may create an agent of this role. The
// session creates its manager and debugger for the user; agents create only
// workers.
func (r Role) Creatable() bool { return r.Worker() }

// Worker reports whether the role carries out assignments for a coordinator.
func (r Role) Worker() bool {
	return r == Implementor || r == Auditor || r == Experimenter || r.Investigates()
}

// Investigates reports whether the role carries out investigations delivered
// as briefs: a review of files on this machine, or research on the web.
func (r Role) Investigates() bool { return r == WebResearcher || r == DeepResearcher || r == Reviewer }

// WorkKinds lists what the role may be assigned. A manager takes
// implementation and repairs only as its own assignee, so a change it makes
// itself is submitted for an independent audit like any other.
func (r Role) WorkKinds() []work.Kind {
	switch r {
	case Implementor:
		return []work.Kind{work.Implementation, work.Repair}
	case Reviewer:
		return []work.Kind{work.Review}
	case WebResearcher:
		return []work.Kind{work.WebResearch}
	case DeepResearcher:
		return []work.Kind{work.DeepResearch}
	case Auditor:
		return []work.Kind{work.AuditWork}
	case Experimenter:
		return []work.Kind{work.Experiment}
	}
	return []work.Kind{}
}
func (r Role) Accepts(k work.Kind) bool {
	return slices.Contains(r.WorkKinds(), k)
}

// RoleFor names the role that does kind work, or "" for an unknown kind. Each
// kind has one role; implementors also take repairs.
func RoleFor(k work.Kind) Role {
	for _, r := range []Role{Implementor, Reviewer, WebResearcher, DeepResearcher, Experimenter, Auditor} {
		if r.Accepts(k) {
			return r
		}
	}
	return ""
}

type CreateRequest struct {
	Role Role `json:"role"`
}
type Registration struct {
	AgentID identity.ActorID `json:"agent_id"`
	Parent  identity.ActorID `json:"parent"`
	Role    Role             `json:"role"`
}
