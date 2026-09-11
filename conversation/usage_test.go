package conversation_test

import (
	"errors"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/provider"
)

func usage(input, output int64) *provider.Usage {
	return &provider.Usage{InputTokens: &input, OutputTokens: &output}
}

func nextUsage(t *testing.T, c *conversation.Controller) conversation.UsageEvent {
	t.Helper()
	return event(t, c, func(e conversation.Event) bool {
		_, ok := e.(conversation.UsageEvent)
		return ok
	}).(conversation.UsageEvent)
}

func TestUsageAcrossToolLoopAndFailure(t *testing.T) {
	c, p := setup(t)
	if _, err := c.Send(c.Root(), "go"); err != nil {
		t.Fatal(err)
	}
	first := p.next(t)
	first.answer <- answer{response: provider.Response{
		Usage:     usage(100, 10),
		ToolCalls: []provider.ToolCall{{ID: "call-usage", Name: "unknown", Arguments: []byte(`{}`)}},
	}}
	second := p.next(t) // The tool error is incorporated before this call.
	e := nextUsage(t, c)
	if e.Agent != c.Root() || e.Observation.Call != 1 || e.Observation.ContextRevision != 2 {
		t.Fatalf("incorrect attribution: %+v", e)
	}
	*e.Observation.Usage.InputTokens = 999
	inspection, err := c.InspectAgent(c.Root(), conversation.InspectOptions{})
	if err != nil || inspection.Usage.InputTokens != 100 || *inspection.Usage.Latest.Usage.InputTokens != 100 {
		t.Fatalf("event mutated accounting: %+v, %v", inspection, err)
	}
	if len(second.request.Messages) != 4 || second.request.Messages[3].ToolCallID != "call-usage" {
		t.Fatalf("usage affected model history: %+v", second.request.Messages)
	}
	second.text("done") // Missing usage must still produce an observation.
	e = nextUsage(t, c)
	if e.Observation.Call != 2 || e.Observation.ContextRevision != 4 || e.Observation.Usage != nil {
		t.Fatal(e)
	}
	userReply(t, c, "done")
	if _, err := c.Send(c.Root(), "again"); err != nil {
		t.Fatal(err)
	}
	third := p.next(t)
	third.answer <- answer{response: provider.Response{
		Content: "must not enter history", Usage: usage(150, 20),
		ToolCalls: []provider.ToolCall{{ID: "rejected", Name: "unknown", Arguments: []byte(`{}`)}},
	}, err: errors.New("rejected completion")}
	e = nextUsage(t, c)
	if e.Observation.Call != 3 || e.Observation.ContextRevision != 6 {
		t.Fatal(e)
	}
	event(t, c, func(e conversation.Event) bool {
		_, ok := e.(conversation.AgentExited)
		return ok
	})
	inspection, err = c.InspectAgent(c.Root(), conversation.InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := inspection.Usage
	if s.Calls != 3 || s.InputTokens != 250 || s.OutputTokens != 30 || s.MissingInputCalls != 1 || s.MissingOutputCalls != 1 {
		t.Fatalf("incorrect post-exit accounting: %+v", s)
	}
	inspection, err = c.InspectAgent(c.Root(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil || len(inspection.Transcript.Entries) != 6 {
		t.Fatalf("rejected output entered history: %+v, %v", inspection.Transcript, err)
	}
}
