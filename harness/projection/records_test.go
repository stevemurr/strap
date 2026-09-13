package projection_test

import (
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// relabel rewrites the envelope's agent, leaving the payload's own agent field
// untouched, so the two disagree.
func relabel(d eventlog.Data, agent identity.ActorID) eventlog.Data {
	d.Agent = string(agent)
	return d
}

// A message needs a stable identity to be addressable, and a receipt is only
// meaningful once its message has been recorded.
func TestProjectorRejectsInconsistentMessagesAndReceipts(t *testing.T) {
	s := newSeeded(t)
	s.reject("message without an identity", s.event(conversation.MessageEvent{Message: message.Message{From: message.User, To: seedAgent, Kind: message.Instruction, Content: "hi"}}))

	sent := message.Message{ID: "m-1", From: message.User, To: seedAgent, Kind: message.Instruction, Content: "hi"}
	s.apply(s.event(conversation.MessageEvent{Message: sent}))
	s.reject("duplicate message", s.event(conversation.MessageEvent{Message: sent}))

	s.reject("receipt before message", s.event(conversation.AckEvent{Receipt: message.Receipt{MessageID: "m-unknown", Recipient: seedAgent, Status: message.Queued}}))
	s.reject("unknown receipt status", s.event(conversation.AckEvent{Receipt: message.Receipt{MessageID: "m-1", Recipient: seedAgent, Status: "teleported"}}))

	s.apply(s.event(conversation.AckEvent{Receipt: message.Receipt{MessageID: "m-1", Recipient: seedAgent, Status: message.Queued}}))
	got, ok := s.p.Receipt("m-1")
	if !ok || got.Recipient != seedAgent || got.Status != message.Queued {
		t.Fatal(got, ok)
	}
	if _, ok := s.p.Receipt("m-unknown"); ok {
		t.Fatal("resolved a receipt for a message that was never recorded")
	}
}

// Usage is reported once per call, in order, and only for a recorded output.
func TestProjectorAccumulatesUsageAndRejectsGaps(t *testing.T) {
	s := newSeeded(t)
	in, out := int64(10), int64(4)
	observation := agent.UsageObservation{Call: 1, ContextRevision: 1, Usage: &provider.Usage{InputTokens: &in, OutputTokens: &out}}
	s.reject("usage call sequence gap", s.event(conversation.UsageEvent{Agent: seedAgent, Observation: agent.UsageObservation{Call: 5}}))
	s.reject("usage for another agent", s.event(conversation.UsageEvent{Agent: "ghost", Observation: observation}))
	s.apply(s.event(conversation.UsageEvent{Agent: seedAgent, Observation: observation}))

	inspection, err := s.p.AgentInspection(seedAgent)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Usage.Calls != 1 || inspection.Usage.InputTokens != in || inspection.Usage.OutputTokens != out {
		t.Fatal("usage totals were not accumulated", inspection.Usage)
	}

	// A call that reports no counts still advances the call sequence, and marks
	// the totals as incomplete rather than silently under-reporting.
	s.apply(s.event(conversation.AgentEvent{Agent: seedAgent, Event: agent.OutputStarted{Output: identity.OutputID{Agent: seedAgent, Call: 2}, ContextRevision: 1, StartedAt: time.Now()}}))
	s.apply(s.event(conversation.UsageEvent{Agent: seedAgent, Observation: agent.UsageObservation{Call: 2, ContextRevision: 1}}))
	inspection, err = s.p.AgentInspection(seedAgent)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Usage.Calls != 2 || inspection.Usage.MissingInputCalls != 1 || inspection.Usage.MissingOutputCalls != 1 {
		t.Fatal("a call without counts was not recorded as incomplete", inspection.Usage)
	}
	if inspection.Usage.InputTokens != in {
		t.Fatal("a call without counts changed the totals", inspection.Usage)
	}
	// Call 3 has no output record behind it.
	s.reject("usage without output", s.event(conversation.UsageEvent{Agent: seedAgent, Observation: agent.UsageObservation{Call: 3}}))
	if _, err := s.p.AgentInspection("ghost"); err != projection.ErrNotFound {
		t.Fatal("unknown agent was not reported as missing", err)
	}
}

// Tool activity is start-then-finish, once, under the agent that began it.
func TestProjectorRejectsInconsistentToolActivity(t *testing.T) {
	s := newSeeded(t)
	now := time.Now()
	activity := func(id string, finished time.Time) agent.ToolActivity {
		return agent.ToolActivity{InvocationID: id, Call: provider.ToolCall{ID: "c1", Name: "shell"}, StartedAt: now, FinishedAt: finished}
	}
	s.reject("tool without an invocation id", s.event(conversation.ToolEvent{Agent: seedAgent, Activity: activity("", time.Time{})}))
	// The envelope's agent and the payload's agent must agree.
	s.reject("tool agent mismatch", relabel(s.event(conversation.ToolEvent{Agent: "ghost", Activity: activity("root/tool-1", time.Time{})}), seedAgent))
	s.reject("finish without a start", s.event(conversation.ToolEvent{Agent: seedAgent, Activity: activity("root/tool-1", now.Add(time.Second))}))

	s.apply(s.event(conversation.ToolEvent{Agent: seedAgent, Activity: activity("root/tool-1", time.Time{})}))
	s.reject("duplicate tool start", s.event(conversation.ToolEvent{Agent: seedAgent, Activity: activity("root/tool-1", time.Time{})}))
	s.apply(s.event(conversation.ToolEvent{Agent: seedAgent, Activity: activity("root/tool-1", now.Add(time.Second))}))
	s.reject("duplicate tool finish", s.event(conversation.ToolEvent{Agent: seedAgent, Activity: activity("root/tool-1", now.Add(2*time.Second))}))
}

// A tool batch and a context measurement both pin themselves to an exact
// history revision, so neither can describe a context that does not exist.
func TestProjectorRejectsBatchAndMeasurementOutsideHistory(t *testing.T) {
	s := newSeeded(t)
	s.reject("empty tool batch", s.event(conversation.ToolBatchEvent{Agent: seedAgent, Batch: agent.ToolBatch{ContextRevision: 1}}))
	s.reject("batch pinned past history", s.event(conversation.ToolBatchEvent{Agent: seedAgent, Batch: agent.ToolBatch{Calls: []string{"c1"}, ContextRevision: 9}}))
	s.apply(s.event(conversation.ToolBatchEvent{Agent: seedAgent, Batch: agent.ToolBatch{Calls: []string{"c1"}, ContextRevision: 1}}))

	s.reject("measurement at revision zero", s.event(conversation.ContextTokensEvent{Agent: seedAgent, Revision: 0, Count: 5}))
	s.reject("measurement past history", s.event(conversation.ContextTokensEvent{Agent: seedAgent, Revision: 9, Count: 5}))
	s.reject("negative measurement", s.event(conversation.ContextTokensEvent{Agent: seedAgent, Revision: 1, Count: -1}))
	s.apply(s.event(conversation.ContextTokensEvent{Agent: seedAgent, Revision: 1, Count: 5}))

	s.reject("commentary agent mismatch", relabel(s.event(conversation.CommentaryEvent{Agent: "ghost", Content: "thinking"}), seedAgent))
	s.apply(s.event(conversation.CommentaryEvent{Agent: seedAgent, Content: "thinking"}))
}

// Work records carry their own identity and a header whose kind, state and
// revision the projection refuses to take on faith.
func TestProjectorRejectsInconsistentWorkRecords(t *testing.T) {
	s := newSeeded(t)
	item := work.Work{ID: "w-1", Kind: work.Implementation, State: work.Active, Revision: 1, Assignee: seedAgent, RequestedBy: seedAgent, Owner: seedAgent, Task: "do it"}
	event := func(id work.EventID, kind work.EventKind, w work.Work) eventlog.Data {
		return s.event(conversation.WorkEvent{Event: work.Event{ID: id, Kind: kind, Actor: seedAgent, Work: w}})
	}
	s.reject("work event without an identity", event("", work.WorkAssigned, item))
	s.reject("unknown work event kind", event("e-1", "teleported", item))

	bad := item
	bad.Revision = 0
	s.reject("work revision zero", event("e-1", work.WorkAssigned, bad))
	bad = item
	bad.Kind = "teleportation"
	s.reject("unknown work kind", event("e-1", work.WorkAssigned, bad))
	bad = item
	bad.State = "teleporting"
	s.reject("unknown work state", event("e-1", work.WorkAssigned, bad))

	s.apply(event("e-1", work.WorkAssigned, item))
	s.reject("duplicate work event", event("e-1", work.WorkAssigned, item))

	v, err := s.p.Work("w-1")
	if err != nil || v.ID != "w-1" || v.Revision != 1 {
		t.Fatal(v, err)
	}
	if v.Through != s.p.Cursor() {
		t.Fatal("work view was not pinned to the projection cursor", v.Through, s.p.Cursor())
	}
	if _, err := s.p.Work("w-unknown"); err != projection.ErrNotFound {
		t.Fatal("unknown work was not reported as missing", err)
	}

	// A revision may advance but never rewind.
	stale := item
	stale.Revision = 0
	s.reject("work revision rewind", event("e-2", work.ProgressChanged, stale))
	forward := item
	forward.Revision = 2
	forward.State = work.NeedsCheck
	s.apply(event("e-2", work.ProgressChanged, forward))
	if v, _ := s.p.Work("w-1"); v.Revision != 2 || v.State != work.NeedsCheck {
		t.Fatal("work view did not advance", v)
	}
}
