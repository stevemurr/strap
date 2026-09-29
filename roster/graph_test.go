package roster

import (
	"strings"
	"testing"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
)

func mustAdd(t *testing.T, g *Graph, id, parent identity.ActorID, role Role) {
	t.Helper()
	if err := g.Add(Registration{AgentID: id, Parent: parent, Role: role}); err != nil {
		t.Fatal(err)
	}
}

// The production topology is a chain: user ⇄ manager ⇄ workers, with
// lifecycle and read access flowing from the manager to its workers only.
// Downward edges start messages; upward, the manager only replies to the user,
// while workers may also message the manager for help.
func TestGraphDerivesTheProductionChain(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "agent-1", User, Manager)
	mustAdd(t, g, "agent-2", "agent-1", Implementor)
	mustAdd(t, g, "agent-3", "agent-1", Auditor)
	want := []string{
		"agent-1 -control-> agent-2", "agent-1 -control-> agent-3",
		"agent-1 -message-> agent-2", "agent-1 -message-> agent-3",
		"agent-1 -read-> agent-2", "agent-1 -read-> agent-3",
		"agent-1 -reply-> user",
		"agent-2 -message-> agent-1", "agent-2 -reply-> agent-1", "agent-3 -message-> agent-1", "agent-3 -reply-> agent-1",
		"user -message-> agent-1",
	}
	if got := g.Edges(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("edges:\n%s", strings.Join(got, "\n"))
	}
	for _, c := range []struct {
		from, to identity.ActorID
		kind     Edge
	}{
		{"agent-2", "agent-3", Message}, // A worker cannot reach another worker.
		{"agent-2", User, Message},      // Nor the user.
		{User, "agent-2", Message},      // The user reaches only the manager.
		{User, "agent-1", Control},      // Nobody controls the manager.
		{"agent-2", "agent-1", Control},
		{"agent-1", "agent-1", Message}, // No agent messages itself.
		{"agent-1", User, Message},      // The manager answers the user only by replying.
	} {
		if g.Allows(c.from, c.to, c.kind) {
			t.Errorf("%s -%s-> %s must not exist", c.from, c.kind, c.to)
		}
	}
	err := g.Check("agent-2", User, Message)
	if err == nil || !strings.Contains(err.Error(), "it can reach only agent-1 (manager)") {
		t.Fatal(err)
	}
	// A manager that messages its report is told whom it can message instead.
	err = g.Check("agent-1", User, Message)
	if err == nil || !strings.Contains(err.Error(), "it can reach only agent-2 (implementor), agent-3 (auditor)") {
		t.Fatal(err)
	}
}

// Replies and failures travel on reply edges; everything an agent starts is a
// message.
func TestEdgeForMessageKinds(t *testing.T) {
	for kind, want := range map[message.MessageKind]Edge{
		message.Reply: Reply, message.Failure: Reply,
		message.Instruction: Message, message.Notification: Message, message.Observation: Message,
	} {
		if got := EdgeFor(kind); got != want {
			t.Errorf("EdgeFor(%s) = %s, want %s", kind, got, want)
		}
	}
}

// A debugger sits beside the manager: the user talks to it directly, it
// talks with the manager, reads every agent including those created after it,
// and controls workers.
func TestGraphDebuggerReachesTheWholeTree(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, "agent-1", User, Manager)
	mustAdd(t, g, "agent-2", User, Debugger)
	mustAdd(t, g, "agent-3", "agent-1", Implementor)
	for _, c := range []struct {
		from, to identity.ActorID
		kind     Edge
		allowed  bool
	}{
		{User, "agent-2", Message, true},
		{"agent-2", User, Reply, true},
		{"agent-2", User, Message, false}, // It answers the user only by replying.
		{"agent-2", "agent-1", Message, true},
		{"agent-1", "agent-2", Message, true},
		{"agent-2", "agent-1", Read, true},
		{"agent-2", "agent-3", Read, true},    // Created after the debugger.
		{"agent-2", "agent-3", Control, true}, // Workers only.
		{"agent-2", "agent-1", Control, false},
		{"agent-2", "agent-3", Message, false}, // Work goes through the manager.
	} {
		if g.Allows(c.from, c.to, c.kind) != c.allowed {
			t.Errorf("%s -%s-> %s: want %v", c.from, c.kind, c.to, c.allowed)
		}
	}
}

// Only the shapes above can be registered.
func TestGraphRefusesOtherShapes(t *testing.T) {
	g := NewGraph()
	for _, r := range []Registration{
		{AgentID: "agent-1", Parent: "agent-9", Role: Manager}, // The manager's parent is the user.
		{AgentID: "agent-1", Parent: User, Role: "root"},       // There is no root.
	} {
		if g.Add(r) == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	mustAdd(t, g, "agent-1", User, Manager)
	for _, r := range []Registration{
		{AgentID: "agent-2", Parent: User, Role: Manager},       // One manager.
		{AgentID: "agent-2", Parent: User, Role: Auditor},       // Workers belong to the manager.
		{AgentID: "agent-2", Parent: "agent-1", Role: Debugger}, // The debugger's parent is the user.
		{AgentID: "agent-1", Parent: "agent-1", Role: Auditor},  // Already registered.
		{AgentID: "agent-2", Parent: "agent-1", Role: "wizard"}, // Unknown role.
		{AgentID: User, Parent: "agent-1", Role: Implementor},   // The user is not an agent.
	} {
		if g.Add(r) == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	mustAdd(t, g, "agent-2", User, Debugger)
	if g.Add(Registration{AgentID: "agent-3", Parent: User, Role: Debugger}) == nil {
		t.Fatal("accepted a second debugger")
	}
	if g.Find(Manager) != "agent-1" || g.Role("agent-2") != Debugger || g.Role("nobody") != Unknown {
		t.Fatal(g.Agents())
	}
}
