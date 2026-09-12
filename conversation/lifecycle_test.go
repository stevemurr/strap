package conversation_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func awaitState(t *testing.T, c *conversation.Controller, id message.ActorID, state agent.State) {
	t.Helper()
	event(t, c, func(e conversation.Event) bool {
		s, ok := e.(conversation.AgentStateChanged)
		return ok && s.Agent == id && s.State == state
	})
}
func TestIdlePauseQueuesInputAndStopDrainsIt(t *testing.T) {
	c, m := setup(t)
	info, err := c.PauseAgent(c.Root())
	if err != nil || info.State != agent.PauseRequested {
		t.Fatalf("pause request: %+v %v", info, err)
	}
	awaitState(t, c, c.Root(), agent.Paused)
	receipt, err := c.Send(c.Root(), "queued while paused")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Receipt(receipt.MessageID); got.Status != message.Queued {
		t.Fatal(got)
	}
	select {
	case <-m.calls:
		t.Fatal("paused agent called model")
	default:
	}
	inspection, err := c.InspectAgent(c.Root(), conversation.InspectOptions{})
	info = inspection.AgentInfo
	if err != nil || info.State != agent.Paused {
		t.Fatalf("inspect: %+v %v", info, err)
	}
	info, err = c.StopAgent(c.Root())
	if err != nil || (info.State != agent.StopRequested && !info.State.Terminal()) {
		t.Fatalf("stop request: %+v %v", info, err)
	}
	event(t, c, func(e conversation.Event) bool {
		exited, ok := e.(conversation.AgentExited)
		return ok && exited.Agent == c.Root()
	})
	if got, _ := c.Receipt(receipt.MessageID); got.Status != message.Undelivered {
		t.Fatal(got)
	}
	if _, err := c.ResumeAgent(c.Root()); err == nil {
		t.Fatal("resumed terminal agent")
	}
	info, err = c.StopAgent(c.Root())
	if err != nil || info.State != agent.Stopped {
		t.Fatalf("repeated stop: %+v %v", info, err)
	}
}

func TestPausePreservesModelResponseAndRemainingToolCalls(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var firstCalls, secondCalls atomic.Int32
	first := tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Parameters: testParameters[struct{}](t), Name: "first"}, Invoke: func(ctx context.Context, _ tool.Call, _ struct{}) (tool.Result, error) {
		firstCalls.Add(1)
		close(entered)
		select {
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		case <-release:
		}
		return tool.Text("first result"), nil
	}}
	second := tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Parameters: testParameters[struct{}](t), Name: "second"}, Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) {
		secondCalls.Add(1)
		return tool.Text("second result"), nil
	}}
	c, m := setup(t, first, second)
	if _, err := c.Send(c.Root(), "work"); err != nil {
		t.Fatal(err)
	}
	pending := m.next(t)
	if _, err := c.PauseAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	pending.answer <- answer{response: provider.Response{ToolCalls: []provider.ToolCall{
		{ID: "first-call", Name: "first", Arguments: json.RawMessage(`{}`)},
		{ID: "second-call", Name: "second", Arguments: json.RawMessage(`{}`)},
	}}}
	awaitState(t, c, c.Root(), agent.Paused)
	if firstCalls.Load() != 0 {
		t.Fatal("tool dispatched before resume")
	}
	queued, err := c.Send(c.Root(), "steer after tools")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResumeAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first tool was not resumed")
	}
	if _, err := c.PauseAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	info, _ := c.InspectAgent(c.Root(), conversation.InspectOptions{})
	if info.State != agent.PauseRequested {
		t.Fatalf("acknowledged pause before operation settled: %+v", info)
	}
	close(release)
	awaitState(t, c, c.Root(), agent.Paused)
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatal("pending tool batch advanced while paused")
	}
	if got, _ := c.Receipt(queued.MessageID); got.Status != message.Queued {
		t.Fatal("consumed inbox during pending batch")
	}
	if _, err := c.ResumeAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	next := m.next(t)
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatal("tool replayed or skipped")
	}
	history := next.request.Messages
	if len(history) != 6 || history[3].Content.Text() != "first result" || history[4].Content.Text() != "second result" || history[5].Envelope.ID != queued.MessageID {
		t.Fatalf("lost checkpoint history: %+v", history)
	}
	next.text("done")
	userReply(t, c, "done")
}

func TestPauseDefersFinalReplyAndResumePendingPause(t *testing.T) {
	c, m := setup(t)
	if _, err := c.Send(c.Root(), "work"); err != nil {
		t.Fatal(err)
	}
	pending := m.next(t)
	if _, err := c.PauseAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	pending.text("preserved answer")
	awaitState(t, c, c.Root(), agent.Paused)
	if _, err := c.ResumeAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	userReply(t, c, "preserved answer")
	if _, err := c.Send(c.Root(), "second"); err != nil {
		t.Fatal(err)
	}
	pending = m.next(t)
	if _, err := c.PauseAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResumeAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	pending.text("not paused")
	userReply(t, c, "not paused")
}
