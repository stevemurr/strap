package conversation_test

import (
	"testing"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/provider"
)

func TestWorkerCommentaryReachesHostWithoutEnteringParentInbox(t *testing.T) {
	c, p := setup(t)
	if _, err := c.Send(c.Root(), "delegate"); err != nil {
		t.Fatal(err)
	}
	p.next(t).tool("create_test_agent", `{"task":"inspect files","expected_output":"findings"}`)
	var root, child call
	for range 2 {
		next := p.next(t)
		if next.request.Agent == c.Root() {
			root = next
		} else {
			child = next
		}
	}
	root.text("Delegated.")
	userReply(t, c, "Delegated.")
	child.answer <- answer{response: provider.Response{
		Content:   "Let me inspect the files.",
		ToolCalls: []provider.ToolCall{{ID: "inspect", Name: "unknown", Arguments: []byte(`{}`)}},
	}}
	seen := false
	event(t, c, func(e conversation.Event) bool {
		switch e := e.(type) {
		case conversation.CommentaryEvent:
			if seen || e.Agent != child.request.Agent || e.Content != "Let me inspect the files." {
				t.Fatalf("unexpected commentary: %+v", e)
			}
			seen = true
		case conversation.ToolEvent:
			if !seen || e.Agent != child.request.Agent || !e.Activity.FinishedAt.IsZero() {
				t.Fatalf("commentary did not precede tool start: %+v", e)
			}
			return true
		case conversation.MessageEvent:
			t.Fatalf("commentary was routed as a message: %+v", e)
		}
		return false
	})
	// Leave the worker's next request pending so it cannot send a final reply.
	if next := p.next(t); next.request.Agent != child.request.Agent {
		t.Fatal("commentary woke parent")
	}
	if _, err := c.Send(c.Root(), "follow up"); err != nil {
		t.Fatal(err)
	}
	next := p.next(t)
	if next.request.Agent != c.Root() || len(next.request.Messages) != 6 {
		t.Fatalf("commentary changed parent history: %+v", next.request)
	}
	if last := next.request.Messages[5].Envelope; last == nil || last.Content != "follow up" {
		t.Fatalf("unexpected parent input: %+v", last)
	}
}
