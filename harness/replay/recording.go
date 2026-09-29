// Package replay reruns a recorded session offline. Each agent's model calls
// are answered with the outputs it recorded, in the recorded order, and every
// call of a tool that reaches outside the session returns its recorded
// result. Everything else, the controller, the topology, the ledger and the
// coordination tools, runs for real: it is the harness under test. Each model
// request is compared with the one the agent sent when it was recorded, and
// the first difference per agent is reported as a divergence.
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/machine"
	"github.com/stevemurr/strap/harness/record"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
)

// Output is one recorded model call.
type Output struct {
	ID       identity.OutputID
	Agent    identity.ActorID
	Context  uint64 // The history revision the request carried: its first Context messages.
	Deltas   []provider.Delta
	Response *provider.Message // The assistant message it produced; nil when it failed.
	Status   agent.OutputStatus
	Err      string
	Rejected *provider.ToolArgumentsError
	Sequence uint64 // The record that finished it, which orders it among the others.
}

// UserMessage is one message the user sent, in order.
type UserMessage struct {
	To       identity.ActorID // The manager, or in a debug session possibly the debugger.
	Text     string
	Sequence uint64
}

// Append is one message added to an agent's history.
type Append struct {
	Agent    identity.ActorID
	Position uint64
	Sequence uint64
}

// Consumption is one inbox message an agent took into its history.
type Consumption struct {
	Agent    identity.ActorID
	ID       message.MessageID
	Sequence uint64
}

// Send is one message the controller admitted from an agent.
type Send struct {
	From     identity.ActorID
	ID       message.MessageID
	Sequence uint64
}

// Activity is a recorded tool call, for traces made before environment records.
type Activity struct {
	Agent  identity.ActorID
	CallID string
	Name   string
	Args   json.RawMessage
	Err    string
}

// Recording is everything a replay needs from a trace.
type Recording struct {
	Path          string
	Session       string
	Seed          []byte // Nil in traces made before seeds: their work ids cannot be reissued.
	Config        harness.EffectiveConfig
	Registrations []roster.Registration
	Histories     map[identity.ActorID][]provider.Message // Position n is index n-1.
	Outputs       []Output                                // In the order they finished.
	Users         []UserMessage
	Sends         []Send                                          // Messages agents and the host sent through the controller, in order.
	Environment   map[string]conversation.EnvironmentEvent        // By invocation.
	EnvironmentAt map[string]uint64                               // Record sequence of each.
	Activities    map[string]Activity                             // By invocation.
	Workspaces    map[identity.ActorID]*message.Workspace         // What each agent's first wake listed.
	Consumed      map[identity.ActorID]map[message.MessageID]bool // Inbox messages each agent took.
	Starts        map[identity.ActorID]map[message.MessageID]bool // Messages that began an exchange: the first each inbox disposition lists.
	Times         map[uint64]time.Time                            // Each record's time, by sequence.
	ToolStarts    map[string]uint64                               // Record sequence of each tool call's start, by invocation.
	Consumptions  []Consumption                                   // Inbox messages taken into history, in order.
	Appends       []Append                                        // History appends not made by a model output, in order.
	TesterRuns    []conversation.TesterEvent                      // Adversarial tester runs, in order.
}

// Open reads a trace.jsonl archive.
func Open(ctx context.Context, path string) (*Recording, error) {
	r := &Recording{Path: path, Histories: map[identity.ActorID][]provider.Message{}, Environment: map[string]conversation.EnvironmentEvent{},
		EnvironmentAt: map[string]uint64{}, Activities: map[string]Activity{}, Workspaces: map[identity.ActorID]*message.Workspace{}, Consumed: map[identity.ActorID]map[message.MessageID]bool{}, Starts: map[identity.ActorID]map[message.MessageID]bool{}, Times: map[uint64]time.Time{}, ToolStarts: map[string]uint64{}}
	outputs := map[identity.OutputID]*Output{}
	var order []identity.OutputID
	configured := false
	err := machine.ReadTrace(ctx, path, func(rec eventlog.Record, e conversation.Event) error {
		r.Times[rec.Sequence] = rec.Time
		if v, ok := e.(conversation.TesterEvent); ok {
			r.TesterRuns = append(r.TesterRuns, v)
			return nil
		}
		switch rec.Kind {
		case "session_started":
			var v struct {
				ID   string `json:"id"`
				Seed []byte `json:"seed"`
			}
			if err := json.Unmarshal(rec.Payload, &v); err != nil {
				return err
			}
			r.Session, r.Seed = v.ID, v.Seed
			return nil
		case "session_configured":
			configured = true
			return json.Unmarshal(rec.Payload, &r.Config)
		case "output_finished":
			var v record.OutputFinished
			if err := json.Unmarshal(rec.Payload, &v); err != nil {
				return err
			}
			o := outputs[v.Output]
			if o == nil {
				return fmt.Errorf("record %d finishes output %v that never started", rec.Sequence, v.Output)
			}
			o.Status, o.Rejected, o.Sequence = v.Status, v.RejectedToolCall, rec.Sequence
			if v.Error != nil {
				o.Err = v.Error.Message
			}
			order = append(order, v.Output)
			return nil
		case "tool":
			var v struct {
				Agent    identity.ActorID `json:"agent"`
				Activity struct {
					Invocation string          `json:"invocation_id"`
					CallID     string          `json:"call_id"`
					Name       string          `json:"name"`
					Arguments  json.RawMessage `json:"arguments"`
					Error      string          `json:"error"`
				} `json:"activity"`
			}
			if err := json.Unmarshal(rec.Payload, &v); err == nil && v.Activity.Invocation != "" {
				a := v.Activity
				if _, seen := r.ToolStarts[a.Invocation]; !seen {
					r.ToolStarts[a.Invocation] = rec.Sequence // A call's first record is its start.
				}
				r.Activities[a.Invocation] = Activity{Agent: v.Agent, CallID: a.CallID, Name: a.Name, Args: a.Arguments, Err: a.Error}
			}
			return nil
		}
		switch v := e.(type) {
		case conversation.AgentRegistered:
			r.Registrations = append(r.Registrations, v.Registration)
		case conversation.MessageEvent:
			m := v.Message
			switch {
			case m.From == message.User:
				r.Users = append(r.Users, UserMessage{To: m.To, Text: m.Content, Sequence: rec.Sequence})
			case m.Kind == message.Failure && strings.HasPrefix(m.Content, fmt.Sprintf("Agent %s failed:", m.From)):
				// The controller reports an agent's failure as the agent exits,
				// outside the send order; the exit itself falls at its recorded
				// output, so the notice needs no place of its own.
			case strings.HasPrefix(string(m.ID), "message-"): // Host notices carry host- ids and bypass the controller.
				r.Sends = append(r.Sends, Send{From: m.From, ID: m.ID, Sequence: rec.Sequence})
			}
		case conversation.AckEvent:
			if v.Receipt.Status == message.Consumed {
				if r.Consumed[v.Receipt.Recipient] == nil {
					r.Consumed[v.Receipt.Recipient] = map[message.MessageID]bool{}
				}
				r.Consumed[v.Receipt.Recipient][v.Receipt.MessageID] = true
				r.Consumptions = append(r.Consumptions, Consumption{Agent: v.Receipt.Recipient, ID: v.Receipt.MessageID, Sequence: rec.Sequence})
			}
		case conversation.EnvironmentEvent:
			r.Environment[v.InvocationID] = v
			r.EnvironmentAt[v.InvocationID] = rec.Sequence
		case conversation.AgentEvent:
			switch f := v.Event.(type) {
			case agent.OutputStarted:
				outputs[f.Output] = &Output{ID: f.Output, Agent: v.Agent, Context: f.ContextRevision}
			case agent.InboxDisposition:
				if len(f.Messages) > 0 {
					if r.Starts[v.Agent] == nil {
						r.Starts[v.Agent] = map[message.MessageID]bool{}
					}
					r.Starts[v.Agent][f.Messages[0]] = true
				}
			case agent.OutputDelta:
				if o := outputs[f.Output]; o != nil {
					o.Deltas = append(o.Deltas, provider.Delta{Channel: f.Channel, Text: f.Text})
				}
			case agent.HistoryAppended:
				h := r.Histories[v.Agent]
				if f.Position != uint64(len(h)+1) {
					return fmt.Errorf("record %d appends %s history at %d after %d messages", rec.Sequence, v.Agent, f.Position, len(h))
				}
				r.Histories[v.Agent] = append(h, f.Message)
				if f.Output == nil { // An output's own message follows its output gate.
					r.Appends = append(r.Appends, Append{Agent: v.Agent, Position: f.Position, Sequence: rec.Sequence})
				}
				if f.Output != nil && f.Message.Role == "assistant" {
					if o := outputs[*f.Output]; o != nil {
						m := f.Message
						o.Response = &m
					}
				}
				if env := f.Message.Envelope; env != nil && env.Workspace != nil {
					if _, seen := r.Workspaces[v.Agent]; !seen {
						w := *env.Workspace
						r.Workspaces[v.Agent] = &w
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, errors.New("trace records no session configuration")
	}
	for _, id := range order {
		r.Outputs = append(r.Outputs, *outputs[id])
	}
	sort.SliceStable(r.Outputs, func(i, j int) bool { return r.Outputs[i].Sequence < r.Outputs[j].Sequence })
	return r, nil
}

// Taken lists the inbox messages the agent took into its history starting at
// position, in order: the run of consumed messages recorded there, within one
// exchange. A message that began an exchange ends the run before it, and is
// taken only by an agent starting an exchange: the recorded agent had settled
// the exchange before it, so one still running takes nothing more (ladder
// easy-04, 2026-09-25, where a running exchange waited for the next one's
// messages). ok is false past the end of the recorded history.
func (r *Recording) Taken(agentID identity.ActorID, position uint64, starting bool) (ids []message.MessageID, ok bool) {
	h := r.Histories[agentID]
	if position == 0 || position > uint64(len(h)) {
		return nil, false
	}
	for i, m := range h[position-1:] {
		if m.Envelope == nil || !r.Consumed[agentID][m.Envelope.ID] {
			break
		}
		if r.Starts[agentID][m.Envelope.ID] && (i > 0 || !starting) {
			break
		}
		ids = append(ids, m.Envelope.ID)
	}
	return ids, true
}

// Role returns a recorded agent's role.
func (r *Recording) Role(id identity.ActorID) roster.Role {
	for _, reg := range r.Registrations {
		if reg.AgentID == id {
			return reg.Role
		}
	}
	return roster.Unknown
}

// RoleConfig returns the recorded configuration of a role.
func (r *Recording) RoleConfig(role roster.Role) (harness.RoleConfiguration, bool) {
	c := r.Config
	switch role {
	case roster.Agent:
		if c.Agent != nil {
			return *c.Agent, true
		}
	case roster.Manager:
		return c.Manager, true
	case roster.Implementor:
		return c.Implementor, true
	case roster.Auditor:
		return c.Auditor, true
	case roster.WebResearcher:
		return c.WebResearcher, true
	case roster.DeepResearcher:
		if c.DeepResearcher != nil {
			return *c.DeepResearcher, true
		}
	case roster.Experimenter:
		if c.Experimenter != nil {
			return *c.Experimenter, true
		}
	case roster.Reviewer:
		if c.Reviewer != nil {
			return *c.Reviewer, true
		}
	case roster.Debugger:
		if c.Debugger != nil {
			return *c.Debugger, true
		}
	}
	return harness.RoleConfiguration{}, false
}
