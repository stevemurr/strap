package conversation_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

func TestTranscriptDuringToolMatchesActualModelHistory(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	blocking := tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Name: "verify", Parameters: testParameters[struct{}](t)}, Invoke: func(ctx context.Context, _ tool.Call, _ struct{}) (tool.Result, error) {
		close(entered)
		select {
		case <-release:
			return tool.Text("verified"), nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	c, p := setup(t, blocking)
	if _, err := c.Send(c.Root(), "check this"); err != nil {
		t.Fatal(err)
	}
	first := p.next(t)
	first.answer <- answer{response: provider.Response{Content: "Checking the output now.", ToolCalls: []provider.ToolCall{{ID: "verify-1", Name: "verify", Arguments: json.RawMessage(`{"input":{}}`)}}}}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	if _, err := c.Send(c.Root(), "queued during tool"); err != nil {
		t.Fatal(err)
	}
	options := conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}}
	in, err := c.InspectAgent(c.Root(), options)
	if err != nil {
		t.Fatal(err)
	}
	if in.State != agent.Running || len(in.Transcript.Entries) != 3 {
		t.Fatalf("unexpected in-flight transcript: %+v", in)
	}
	last := in.Transcript.Entries[2].Message
	if last.Content.Text() != "Checking the output now." || last.ToolCalls[0].ID != "verify-1" {
		t.Fatal("assistant tool-call text missing")
	}
	repeated, _ := c.InspectAgent(c.Root(), options)
	if !reflect.DeepEqual(in.Transcript, repeated.Transcript) {
		t.Fatal("inspection changed thread")
	}
	close(release)
	next := p.next(t)
	after, err := c.InspectAgent(c.Root(), options)
	if err != nil {
		t.Fatal(err)
	}
	var actual []provider.Message
	for _, entry := range after.Transcript.Entries {
		actual = append(actual, entry.Message)
	}
	if !reflect.DeepEqual(actual, next.request.Messages) {
		t.Fatalf("inspection diverged from model history\n%+v\n%+v", actual, next.request.Messages)
	}
	if len(actual) != 5 || actual[3].ToolCallID != "verify-1" || actual[3].Content.Text() != "verified" || actual[4].Envelope.Content != "queued during tool" {
		t.Fatal(actual)
	}
	next.text("done")
	event(t, c, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.To == message.User && m.Message.Content == "done"
	})
	if _, err := c.StopAgent(c.Root()); err != nil {
		t.Fatal(err)
	}
	awaitState(t, c, c.Root(), agent.Stopped)
	stopped, err := c.InspectAgent(c.Root(), options)
	if err != nil || len(stopped.Transcript.Entries) != 6 {
		t.Fatalf("stopped transcript unavailable: %+v %v", stopped, err)
	}
	if _, err := c.InspectAgent("missing", options); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestAgentCanInspectItsOwnThreadWithoutDeadlockOrRewritingHistory(t *testing.T) {
	var controller *conversation.Controller
	inspect := tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Name: "inspect_self", Parameters: testParameters[struct{}](t)}, Invoke: func(_ context.Context, call tool.Call, _ struct{}) (tool.Result, error) {
		in, err := controller.InspectAgent(call.Actor, conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
		if err != nil {
			return tool.Result{}, err
		}
		return tool.JSON(in)
	}}
	c, p := setup(t, inspect)
	controller = c
	if _, err := c.Send(c.Root(), "inspect yourself"); err != nil {
		t.Fatal(err)
	}
	first := p.next(t)
	first.tool("inspect_self", `{"input":{}}`)
	next := p.next(t)
	history := next.request.Messages
	if len(history) != 4 || history[3].Role != "tool" {
		t.Fatal("self-inspection did not settle")
	}
	var returned conversation.AgentInspection
	if err := json.Unmarshal([]byte(history[3].Content.Text()), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.Transcript == nil || len(returned.Transcript.Entries) != 3 {
		t.Fatal("self-inspection did not return pre-result snapshot")
	}
	current, err := c.InspectAgent(c.Root(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{}})
	if err != nil || len(current.Transcript.Entries) != 4 || current.Transcript.Entries[3].Message.Content.Text() != history[3].Content.Text() {
		t.Fatal("canonical thread did not retain actual inspection result")
	}
	next.text("inspection complete")
}
