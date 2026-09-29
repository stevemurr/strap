// Package machine checks a session against the harness's state machine. A
// Recorder turns a session's events into a Timeline, live or from a recorded
// trace; Violations states what is allowed to happen and checks the Timeline
// against it, and Paths renders the machine as it ran.
package machine

import (
	"slices"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// Intent is what a user message asks of the manager, which decides what its
// answer must contain.
type Intent string

const (
	Task      Intent = "task"      // Work on the project.
	Update    Intent = "update"    // Changes work in progress.
	Answer    Intent = "answer"    // Answers a question the manager asked.
	FollowUp  Intent = "follow-up" // More work after a report.
	Question  Intent = "question"  // How the work is going; the manager knows the state.
	Chat      Intent = "chat"      // Conversation needing no project knowledge.
	Ambiguous Intent = "ambiguous" // A change that leaves out what to change: answered with a question.
	Research  Intent = "research"  // Asks for something the manager must investigate, outside facts or the code's behavior: answered from delivered research or an experiment.
	Unknown   Intent = "unknown"   // A recorded user message nobody classified.
)

// Turn is one user message to the manager.
type Turn struct {
	At     time.Duration
	Text   string
	Intent Intent
}

// Message is a message as it was sent.
type Message struct {
	At time.Duration
	message.Message
}

// Call is one finished tool call.
type Call struct {
	At    time.Duration // When the call finished.
	Start time.Duration // When it started; a call's own messages fall between.
	Agent identity.ActorID
	Name  string
	Args  string
	Err   string
}

// Output is one model response an agent committed to its history.
type Output struct {
	At    time.Duration
	Agent identity.ActorID
	Text  string
	Calls []string // Tool names, in order.
}

// Write is one workspace file an agent's call created, changed or deleted.
type Write struct {
	At    time.Duration
	Agent identity.ActorID
	Path  string // Relative to the workspace.
	Tool  string
}

// Exit is an agent reaching stopped or failed.
type Exit struct {
	At    time.Duration
	ID    identity.ActorID
	State agent.State
}

// WorkChange and StepChange are one recorded transition of a work item or a
// plan step; From is "" when the record is new.
type WorkChange struct {
	At              time.Duration
	ID, Parent      work.ID
	Kind            work.Kind
	Owner, Assignee identity.ActorID
	From, To        work.State
	Event           work.EventKind
}

type StepChange struct {
	At       time.Duration
	Plan     work.PlanID
	Step     work.StepID
	Owner    identity.ActorID
	From, To work.StepStatus
}

// Timeline is what happened in a session, in order, detached from the
// session so rules can be checked offline and on hand-built histories.
type Timeline struct {
	Manager       identity.ActorID // The agent the user talks to.
	Roles         map[identity.ActorID]roster.Role
	Parent        map[identity.ActorID]identity.ActorID
	Registrations []roster.Registration // In order, to rebuild the topology.
	Messages      []Message
	Calls         []Call
	Outputs       []Output
	Writes        []Write
	WorkLog       []WorkChange
	StepLog       []StepChange
	Exits         []Exit
	Turns         []Turn
	Works         map[work.ID]work.Work     // Latest state.
	Plans         map[work.PlanID]work.Plan // Latest state.
}

// NewTimeline returns an empty timeline for the session whose root is root.
func NewTimeline(manager identity.ActorID) Timeline {
	return Timeline{Manager: manager, Roles: map[identity.ActorID]roster.Role{}, Parent: map[identity.ActorID]identity.ActorID{},
		Works: map[work.ID]work.Work{}, Plans: map[work.PlanID]work.Plan{}}
}

// Clone returns a deep copy.
func (tl Timeline) Clone() Timeline {
	out := NewTimeline(tl.Manager)
	out.Registrations, out.Messages, out.Calls, out.Outputs = slices.Clone(tl.Registrations), slices.Clone(tl.Messages), slices.Clone(tl.Calls), slices.Clone(tl.Outputs)
	out.Writes = slices.Clone(tl.Writes)
	out.WorkLog, out.StepLog, out.Exits, out.Turns = slices.Clone(tl.WorkLog), slices.Clone(tl.StepLog), slices.Clone(tl.Exits), slices.Clone(tl.Turns)
	for i, m := range out.Messages {
		out.Messages[i].Message = m.Message.Clone()
	}
	for k, v := range tl.Roles {
		out.Roles[k] = v
	}
	for k, v := range tl.Parent {
		out.Parent[k] = v
	}
	for k, v := range tl.Works {
		out.Works[k] = v.Clone()
	}
	for k, v := range tl.Plans {
		out.Plans[k] = v.Clone()
	}
	return out
}

// Managers lists the agents the user talks to, sorted: the session's manager,
// or a solo session's agent.
func (tl Timeline) Managers() []identity.ActorID {
	var out []identity.ActorID
	for id, role := range tl.Roles {
		if role == roster.Manager || role == roster.Agent {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// RepliesToUser lists the manager's replies to the user at or after after.
func (tl Timeline) RepliesToUser(after time.Duration) []Message {
	var out []Message
	for _, msg := range tl.Messages {
		if msg.At >= after && msg.From == tl.Manager && msg.To == message.User {
			out = append(out, msg)
		}
	}
	return out
}

// StateAt replays the work log up to t: each work item's latest change.
func (tl Timeline) StateAt(t time.Duration) map[work.ID]WorkChange {
	out := map[work.ID]WorkChange{}
	for _, c := range tl.WorkLog {
		if c.At <= t {
			out[c.ID] = c
		}
	}
	return out
}
