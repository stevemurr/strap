package roster

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
)

// Edge is a capability one agent holds over another.
type Edge string

const (
	Message Edge = "message" // May send it a message.
	Reply   Edge = "reply"   // May answer it: its final replies and failure reach it.
	Control Edge = "control" // May stop, pause and resume it.
	Read    Edge = "read"    // May read its state, transcript and work.
)

// EdgeFor is the edge a message of kind needs: an agent's final reply and its
// failure answer whoever it works for; anything it starts is a message.
func EdgeFor(kind message.MessageKind) Edge {
	if kind == message.Reply || kind == message.Failure {
		return Reply
	}
	return Message
}

// User is the human at the conversation's entry point. It is a node in the
// graph but never a registered agent.
const User identity.ActorID = "user"

// Graph is the session's agent topology: every agent, its role and parent, and
// the typed edges between them. Edges are never added directly. Registering an
// agent derives them from its role and parent, so the graph is a pure
// function of the registration sequence: the harness enforces it at runtime,
// and a replay of the event log rebuilds exactly the same graph.
//
//	user → manager                   message
//	manager → user                   reply      (it answers by replying, never by messaging)
//	manager → worker                 message, control, read
//	worker → manager                 message, reply   (a worker may ask for help mid-task)
//	user → debugger                  message    (debug sessions only)
//	debugger → user                  reply
//	debugger ⇄ manager               message
//	debugger → every other agent     read
//	debugger → worker                control
type Graph struct {
	mu    sync.RWMutex
	nodes map[identity.ActorID]Registration
	order []identity.ActorID
	edges map[identity.ActorID]map[identity.ActorID]map[Edge]bool
}

func NewGraph() *Graph {
	return &Graph{nodes: map[identity.ActorID]Registration{}, edges: map[identity.ActorID]map[identity.ActorID]map[Edge]bool{}}
}

// Admit reports whether r could be added, without adding it.
func (g *Graph) Admit(r Registration) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.admitLocked(r)
}

func (g *Graph) admitLocked(r Registration) error {
	if r.AgentID == "" || r.AgentID == User {
		return fmt.Errorf("invalid agent id %q", r.AgentID)
	}
	if _, ok := g.nodes[r.AgentID]; ok {
		return fmt.Errorf("agent %s already registered", r.AgentID)
	}
	parent, known := g.nodes[r.Parent]
	switch r.Role {
	case Manager, Debugger, Agent:
		if r.Parent != User || g.hasRoleLocked(r.Role) {
			return fmt.Errorf("the %s's parent is the user, and there is one %s", r.Role, r.Role)
		}
	case Implementor, Auditor, WebResearcher, DeepResearcher, Experimenter, Reviewer:
		if !known || parent.Role != Manager {
			return fmt.Errorf("a %s's parent is the manager", r.Role)
		}
	default:
		return fmt.Errorf("unknown role %q", r.Role)
	}
	return nil
}

// Add registers an agent and derives its edges. The manager and the debugger
// have the user as parent, one of each; every worker's parent must already be
// registered as the manager.
func (g *Graph) Add(r Registration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.admitLocked(r); err != nil {
		return err
	}
	g.nodes[r.AgentID] = r
	g.order = append(g.order, r.AgentID)
	switch r.Role {
	case Manager, Agent:
		g.linkLocked(User, r.AgentID, Message)
		g.linkLocked(r.AgentID, User, Reply)
		if d := g.roleLocked(Debugger); d != "" {
			g.linkLocked(d, r.AgentID, Message, Read)
			g.linkLocked(r.AgentID, d, Message)
		}
	case Debugger:
		g.linkLocked(User, r.AgentID, Message)
		g.linkLocked(r.AgentID, User, Reply)
		for _, id := range g.order {
			if id == r.AgentID {
				continue
			}
			g.linkLocked(r.AgentID, id, Read)
			switch g.nodes[id].Role {
			case Manager, Agent:
				g.linkLocked(r.AgentID, id, Message)
				g.linkLocked(id, r.AgentID, Message)
			case Implementor, Auditor, WebResearcher, DeepResearcher, Experimenter, Reviewer:
				g.linkLocked(r.AgentID, id, Control)
			}
		}
	default: // Workers.
		g.linkLocked(r.Parent, r.AgentID, Message, Control, Read)
		g.linkLocked(r.AgentID, r.Parent, Message, Reply)
		if d := g.roleLocked(Debugger); d != "" {
			g.linkLocked(d, r.AgentID, Read, Control)
		}
	}
	return nil
}

func (g *Graph) linkLocked(from, to identity.ActorID, kinds ...Edge) {
	if g.edges[from] == nil {
		g.edges[from] = map[identity.ActorID]map[Edge]bool{}
	}
	if g.edges[from][to] == nil {
		g.edges[from][to] = map[Edge]bool{}
	}
	for _, k := range kinds {
		g.edges[from][to][k] = true
	}
}

func (g *Graph) hasRoleLocked(role Role) bool { return g.roleLocked(role) != "" }

func (g *Graph) roleLocked(role Role) identity.ActorID {
	for _, id := range g.order {
		if g.nodes[id].Role == role {
			return id
		}
	}
	return ""
}

// Allows reports whether from holds a kind edge to to.
func (g *Graph) Allows(from, to identity.ActorID, kind Edge) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edges[from][to][kind]
}

// Check returns an error naming whom from may reach when it lacks the edge.
func (g *Graph) Check(from, to identity.ActorID, kind Edge) error {
	if g.Allows(from, to, kind) {
		return nil
	}
	reach := g.Neighbors(from, kind)
	names := make([]string, len(reach))
	for i, id := range reach {
		names[i] = g.Describe(id)
	}
	if len(names) == 0 {
		return fmt.Errorf("%s has no %s edge to %s, or to any agent", g.Describe(from), kind, g.Describe(to))
	}
	return fmt.Errorf("%s has no %s edge to %s; it can reach only %s", g.Describe(from), kind, g.Describe(to), strings.Join(names, ", "))
}

// Neighbors lists the agents from holds a kind edge to, in registration order.
func (g *Graph) Neighbors(from identity.ActorID, kind Edge) []identity.ActorID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []identity.ActorID
	for to, kinds := range g.edges[from] {
		if kinds[kind] {
			out = append(out, to)
		}
	}
	rank := map[identity.ActorID]int{User: -1}
	for i, id := range g.order {
		rank[id] = i
	}
	sort.Slice(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}

// Registration returns an agent's registration.
func (g *Graph) Registration(id identity.ActorID) (Registration, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	r, ok := g.nodes[id]
	return r, ok
}

// Role returns an agent's role, or Unknown.
func (g *Graph) Role(id identity.ActorID) Role {
	if r, ok := g.Registration(id); ok {
		return r.Role
	}
	return Unknown
}

// Find returns the agent with a role that has one agent per session, such as
// the manager, or "".
func (g *Graph) Find(role Role) identity.ActorID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.roleLocked(role)
}

// Agents lists registrations in registration order.
func (g *Graph) Agents() []Registration {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Registration, len(g.order))
	for i, id := range g.order {
		out[i] = g.nodes[id]
	}
	return out
}

// Describe names an agent with its role, such as "agent-2 (manager)".
func (g *Graph) Describe(id identity.ActorID) string {
	if id == User {
		return "the user"
	}
	if r, ok := g.Registration(id); ok {
		return fmt.Sprintf("%s (%s)", id, r.Role)
	}
	return string(id)
}

// Edges lists every edge as "from -kind-> to", sorted, for inspection.
func (g *Graph) Edges() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []string
	for from, tos := range g.edges {
		for to, kinds := range tos {
			for k := range kinds {
				out = append(out, fmt.Sprintf("%s -%s-> %s", from, k, to))
			}
		}
	}
	slices.Sort(out)
	return out
}
