package projection

import (
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

type AgentInfo struct {
	conversation.AgentInfo
	Role              roster.Role `json:"role"`
	Registered        bool        `json:"registered"`
	EligibleWorkKinds []work.Kind `json:"eligible_work_kinds"`
	ActiveWorkIDs     []work.ID   `json:"active_work_ids"`
}
type AgentInspection struct {
	conversation.AgentInspection
	Role              roster.Role `json:"role"`
	Registered        bool        `json:"registered"`
	EligibleWorkKinds []work.Kind `json:"eligible_work_kinds"`
	ActiveWorkIDs     []work.ID   `json:"active_work_ids"`
}

func (p *Projector) Registration(id identity.ActorID) (roster.Registration, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.registrations[id]
	return r, ok
}
func (p *Projector) Enrich(a conversation.AgentInspection, works []work.Work) AgentInspection {
	r, ok := p.Registration(a.ID)
	if !ok {
		r.Role = roster.Unknown
	}
	out := AgentInspection{AgentInspection: a, Role: r.Role, Registered: ok, EligibleWorkKinds: r.Role.WorkKinds(), ActiveWorkIDs: []work.ID{}}
	for _, w := range works {
		if w.Assignee == a.ID && w.State == work.Active {
			out.ActiveWorkIDs = append(out.ActiveWorkIDs, w.ID)
		}
	}
	return out
}
func (a AgentInspection) Info() AgentInfo {
	return AgentInfo{AgentInfo: a.AgentInfo, Role: a.Role, Registered: a.Registered, EligibleWorkKinds: a.EligibleWorkKinds, ActiveWorkIDs: a.ActiveWorkIDs}
}
