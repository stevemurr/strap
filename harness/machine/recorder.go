package machine

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

// Transition is one behavioural change worth reading in order: an agent
// created, a message sent, a tool call finished, a plan step or work item
// changing state. At is seconds since the session started.
type Transition struct {
	At     float64 `json:"at"`
	Kind   string  `json:"kind"`
	Agent  string  `json:"agent,omitempty"`
	Detail string  `json:"detail"`
}

// Recorder folds a session's events into its Timeline and keeps the live
// state a caller waits on. It is not safe for concurrent use.
type Recorder struct {
	Timeline
	State     map[identity.ActorID]agent.State
	Open      map[string]time.Duration // Open tool calls and when they started.
	Pending   map[message.MessageID]identity.ActorID
	Consumed  map[message.MessageID]time.Duration
	Completed map[work.StepID]time.Duration
	Accepted  map[work.ID]time.Duration
	Holding   map[identity.ActorID]bool // Agents given a user message they have not answered yet.
	// Dir is the workspace, for traces that predate environment records,
	// whose writes are known only from write_file and edit_file arguments.
	Dir         string
	environment bool // The trace records environment calls and their changes.
	Replied     bool // An agent answered the user after the latest user message.
}

func NewRecorder(manager identity.ActorID) *Recorder {
	return &Recorder{Timeline: NewTimeline(manager), State: map[identity.ActorID]agent.State{}, Open: map[string]time.Duration{},
		Pending: map[message.MessageID]identity.ActorID{}, Consumed: map[message.MessageID]time.Duration{},
		Completed: map[work.StepID]time.Duration{}, Accepted: map[work.ID]time.Duration{}, Holding: map[identity.ActorID]bool{}}
}

// Snapshot returns a detached copy of the timeline.
func (r *Recorder) Snapshot() Timeline { return r.Timeline.Clone() }

func transition(at time.Duration, kind string, actor identity.ActorID, format string, args ...any) Transition {
	return Transition{At: at.Round(10 * time.Millisecond).Seconds(), Kind: kind, Agent: string(actor), Detail: fmt.Sprintf(format, args...)}
}

// Turn records a user message to the manager and its intent.
func (r *Recorder) Turn(at time.Duration, text string, kind Intent) Transition {
	r.Turns = append(r.Turns, Turn{At: at, Text: text, Intent: kind})
	return transition(at, "user", "user", "→ manager (%s): %s", kind, text)
}

// Name labels an agent with its role.
func (r *Recorder) Name(id identity.ActorID) string {
	if role, ok := r.Roles[id]; ok {
		return fmt.Sprintf("%s(%s)", id, role)
	}
	return string(id)
}

// Clip flattens whitespace and cuts s to n bytes.
func Clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Observe folds one event, which happened at at, into the recorder and returns
// the transitions it made.
func (r *Recorder) Observe(at time.Duration, e conversation.Event) []Transition {
	var out []Transition
	record := func(kind string, actor identity.ActorID, format string, args ...any) {
		out = append(out, transition(at, kind, actor, format, args...))
	}
	switch v := e.(type) {
	case conversation.AgentRegistered:
		reg := v.Registration
		r.Roles[reg.AgentID], r.Parent[reg.AgentID] = reg.Role, reg.Parent
		r.Registrations = append(r.Registrations, reg)
		record("agent", reg.AgentID, "created %s by %s", reg.Role, r.Name(reg.Parent))
	case conversation.AgentStateChanged:
		if r.State[v.Agent] != v.State {
			r.State[v.Agent] = v.State
			if v.State == agent.Stopped || v.State == agent.Failed {
				r.Exits = append(r.Exits, Exit{At: at, ID: v.Agent, State: v.State})
				record("state", v.Agent, "%s %s", r.Name(v.Agent), v.State)
			}
		}
	case conversation.AgentExited:
		r.State[v.Agent] = agent.Stopped
	case conversation.MessageEvent:
		msg := v.Message
		r.Messages = append(r.Messages, Message{at, msg.Clone()})
		if msg.To != message.User {
			r.Pending[msg.ID] = msg.To
		}
		if msg.From == message.User {
			r.Holding[msg.To] = true
			r.Replied = false
		}
		if msg.To == message.User && (msg.Kind == message.Reply || msg.Kind == message.Failure) {
			delete(r.Holding, msg.From)
			r.Replied = true
		}
		if msg.Kind == message.Observation {
			return out
		}
		body := msg.Content
		switch {
		case msg.Work != nil:
			body = fmt.Sprintf("assignment %s %s: %s", msg.Work.ID, msg.Work.Kind, msg.Work.Task)
		case msg.Event != nil:
			body = fmt.Sprintf("work event %s on %s", msg.Event.Kind, msg.Event.Work.ID)
		case msg.Progress != nil && body == "":
			body = "progress notice"
		}
		record("message", msg.From, "%s → %s %s: %s", r.Name(msg.From), r.Name(msg.To), msg.Kind, Clip(body, 160))
	case conversation.AgentEvent:
		h, ok := v.Event.(agent.HistoryAppended)
		if !ok || h.Output == nil || h.Message.Role != "assistant" {
			return out
		}
		o := Output{At: at, Agent: v.Agent, Text: h.Message.Content.Text()}
		for _, c := range h.Message.ToolCalls {
			o.Calls = append(o.Calls, c.Name)
		}
		r.Outputs = append(r.Outputs, o)
		if len(o.Calls) == 0 {
			record("output", v.Agent, "text: %s", Clip(o.Text, 160))
		} else {
			record("output", v.Agent, "calls %s", strings.Join(o.Calls, ", "))
		}
	case conversation.EnvironmentEvent:
		r.environment = true
		changed := v.Changed
		if len(changed) == 0 && !v.Scanned && !v.Unscanned && v.Err == "" && (v.Name == "write_file" || v.Name == "edit_file") {
			// Records made before changes were captured name the file only
			// in the arguments.
			if p := r.argumentPath(string(v.Arguments)); p != "" {
				changed = []string{p}
			}
		}
		for _, p := range changed {
			r.Writes = append(r.Writes, Write{At: at, Agent: v.Agent, Path: p, Tool: v.Name})
			record("write", v.Agent, "%s %s", v.Name, p)
		}
		if v.Unscanned {
			record("write", v.Agent, "%s changed the workspace unscanned", v.Name)
		}
	case conversation.AckEvent:
		switch v.Receipt.Status {
		case message.Consumed:
			r.Consumed[v.Receipt.MessageID] = at
			delete(r.Pending, v.Receipt.MessageID)
		case message.Undelivered:
			delete(r.Pending, v.Receipt.MessageID)
		}
	case conversation.ToolEvent:
		a := v.Activity
		if a.FinishedAt.IsZero() {
			r.Open[a.InvocationID] = at
			return out
		}
		start, ok := r.Open[a.InvocationID]
		if !ok {
			start = at
		}
		delete(r.Open, a.InvocationID)
		c := Call{At: at, Start: start, Agent: v.Agent, Name: a.Call.Name, Args: string(a.Call.Arguments)}
		status := "ok"
		if a.Err != nil {
			c.Err, status = a.Err.Error(), "error: "+Clip(a.Err.Error(), 120)
		}
		r.Calls = append(r.Calls, c)
		if !r.environment && c.Err == "" && (c.Name == "write_file" || c.Name == "edit_file") {
			if p := r.argumentPath(c.Args); p != "" {
				r.Writes = append(r.Writes, Write{At: at, Agent: v.Agent, Path: p, Tool: c.Name})
			}
		}
		record("tool", v.Agent, "%s %s %s", a.Call.Name, Clip(c.Args, 100), status)
	case conversation.WorkEvent:
		ev := v.Event
		plans := ev.Changes().Plans
		if ev.Plan != nil {
			plans = append(plans, *ev.Plan)
		}
		for _, p := range plans {
			old, seen := r.Plans[p.ID]
			if !seen {
				record("plan", p.Owner, "created %s %q owned by %s with %d steps", p.ID, p.Title, r.Name(p.Owner), len(p.Steps))
			}
			before := map[work.StepID]work.StepStatus{}
			for _, s := range old.Steps {
				before[s.ID] = s.Status
			}
			for _, s := range p.Steps {
				if prior, ok := before[s.ID]; !ok || prior != s.Status {
					r.StepLog = append(r.StepLog, StepChange{At: at, Plan: p.ID, Step: s.ID, Owner: p.Owner, From: prior, To: s.Status})
					if !ok {
						prior = "new"
					}
					record("step", p.Owner, "%s %q %s → %s", s.ID, Clip(s.Title, 60), prior, s.Status)
				}
				if s.Status == work.Completed {
					if _, ok := r.Completed[s.ID]; !ok {
						r.Completed[s.ID] = at
					}
				}
			}
			r.Plans[p.ID] = p.Clone()
		}
		works := ev.Changes().Works
		if ev.Work.ID != "" {
			works = append(works, ev.Work)
		}
		for _, w := range works {
			old, seen := r.Works[w.ID]
			// A reassignment keeps the state and changes the assignee; it is a
			// transition too. A manager that adopted a failed worker's work was
			// otherwise recorded as editing without holding it.
			if !seen || old.State != w.State || old.Assignee != w.Assignee {
				r.WorkLog = append(r.WorkLog, WorkChange{At: at, ID: w.ID, Parent: w.ParentID, Kind: w.Kind, Owner: w.Owner, Assignee: w.Assignee, From: old.State, To: w.State, Event: ev.Kind})
				from := "new"
				if seen {
					from = string(old.State)
				}
				record("work", w.Owner, "%s %s for %s owned by %s: %s → %s (%s)", w.ID, w.Kind, r.Name(w.Assignee), r.Name(w.Owner), from, w.State, ev.Kind)
			}
			if w.State == work.Accepted {
				if _, ok := r.Accepted[w.ID]; !ok {
					r.Accepted[w.ID] = at
				}
			}
			r.Works[w.ID] = w.Clone()
		}
	}
	return out
}

// argumentPath reads a write's path from its arguments, relative to Dir.
func (r *Recorder) argumentPath(args string) string {
	var in struct {
		Input struct {
			Path string `json:"path"`
		} `json:"input"`
	}
	if json.Unmarshal([]byte(args), &in) != nil || in.Input.Path == "" {
		return ""
	}
	p := filepath.ToSlash(filepath.Clean(in.Input.Path))
	if dir := filepath.ToSlash(filepath.Clean(r.Dir)); r.Dir != "" && strings.HasPrefix(p, dir+"/") {
		p = strings.TrimPrefix(p, dir+"/")
	}
	return p
}

// Busy reports whether a tool call is open, an agent is running, or a message
// waits for an agent that will read it.
func (r *Recorder) Busy() bool {
	if len(r.Open) > 0 {
		return true
	}
	for _, s := range r.State {
		if s == agent.Running || s == agent.PauseRequested {
			return true
		}
	}
	for _, to := range r.Pending {
		if s := r.State[to]; s != agent.Paused && !s.Terminal() {
			return true
		}
	}
	return false
}

// Settled: the manager answered the latest user message, no agent still owes
// the user an answer, and nothing is running or queued.
func (r *Recorder) Settled() bool {
	return r.Replied && len(r.Holding) == 0 && !r.Busy()
}
