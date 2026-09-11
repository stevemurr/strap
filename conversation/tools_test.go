package conversation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/tool"
)

func toolEvent(t *testing.T, c *conversation.Controller, finished bool) conversation.ToolEvent {
	t.Helper()
	return event(t, c, func(e conversation.Event) bool {
		te, ok := e.(conversation.ToolEvent)
		return ok && !te.Activity.FinishedAt.IsZero() == finished
	}).(conversation.ToolEvent)
}

func TestToolEventsBracketExecutionAndOwnTheirPayload(t *testing.T) {
	release := make(chan struct{})
	called := make(chan string, 1)
	type echoArgs struct {
		Value string `json:"value"`
	}
	echo := tool.Func[echoArgs]{
		Spec: tool.Definition[echoArgs]{Parameters: testParameters[echoArgs](t), Name: "echo"},
		Invoke: func(ctx context.Context, call tool.Call, _ echoArgs) (tool.Result, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
			called <- string(call.Arguments)
			return tool.Result{Content: content.Content{{Text: "result"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}}}}, nil
		},
	}
	c, p := setup(t, echo)
	_, _ = c.Send(c.Root(), "begin")
	p.next(t).tool("echo", `{"value":"original"}`)
	start := toolEvent(t, c, false)
	if start.Agent != c.Root() || start.Activity.Call.ID != "call-1" || start.Activity.StartedAt.IsZero() {
		t.Fatal(start)
	}
	// The notification is available while the tool is blocked, and its arguments
	// are not the tool's arguments or the agent's retained assistant tool call.
	start.Activity.Call.Arguments[0] = 'x'
	close(release)
	finish := toolEvent(t, c, true)
	if got := <-called; got != `{"value":"original"}` {
		t.Fatal(got)
	}
	if finish.Activity.Err != nil || finish.Activity.Call.ID != start.Activity.Call.ID || finish.Activity.StartedAt != start.Activity.StartedAt || finish.Activity.FinishedAt.Before(start.Activity.StartedAt) {
		t.Fatal(finish)
	}
	finish.Activity.Result.Content[0].Text = "mutated"
	finish.Activity.Result.Content[1].Image.Data[0] = 99
	next := p.next(t)
	if len(next.request.Messages) != 4 {
		t.Fatalf("telemetry leaked into model history: %+v", next.request.Messages)
	}
	result := next.request.Messages[3]
	if result.Role != "tool" || result.Content.Text() != "result" || result.Content[1].Image.Data[0] != 1 {
		t.Fatal(result)
	}
	if string(next.request.Messages[2].ToolCalls[0].Arguments) != `{"value":"original"}` {
		t.Fatal("start event mutated history")
	}
	next.text("done")
	userReply(t, c, "done")
}

func TestToolEventsReportFailuresAndCancellation(t *testing.T) {
	for _, name := range []string{"broken", "unknown", "canceled"} {
		t.Run(name, func(t *testing.T) {
			c, p := setup(t, tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Parameters: testParameters[struct{}](t), Name: name}, Invoke: func(ctx context.Context, _ tool.Call, _ struct{}) (tool.Result, error) {
				if name == "canceled" {
					<-ctx.Done()
					return tool.Result{}, ctx.Err()
				}
				return tool.Result{}, errors.New("tool broke")
			}})
			_, _ = c.Send(c.Root(), "begin")
			requested := name
			if name == "unknown" {
				requested = "not_registered"
			}
			p.next(t).tool(requested, `{}`)
			start := toolEvent(t, c, false)
			if name == "canceled" {
				if _, err := c.StopAgent(c.Root()); err != nil {
					t.Fatal(err)
				}
			}
			finish := toolEvent(t, c, true)
			if finish.Activity.Call.Name != requested || finish.Activity.Call.ID != start.Activity.Call.ID || finish.Activity.Err == nil {
				t.Fatal(finish)
			}
			if name == "canceled" {
				if !errors.Is(finish.Activity.Err, context.Canceled) {
					t.Fatal(finish.Activity.Err)
				}
				event(t, c, func(e conversation.Event) bool { _, ok := e.(conversation.AgentExited); return ok })
				info, err := c.InspectAgent(c.Root(), conversation.InspectOptions{})
				if err != nil || info.State != agent.Stopped {
					t.Fatal(info, err)
				}
			} else {
				next := p.next(t)
				if got := next.request.Messages[len(next.request.Messages)-1]; got.Role != "tool" || got.Content.Text() == "" {
					t.Fatal(got)
				}
				next.text("handled")
				userReply(t, c, "handled")
			}
		})
	}
}
