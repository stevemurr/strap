package conversation_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func systemPrompt(t *testing.T, c call) prompt.Prompt {
	t.Helper()
	if len(c.request.Messages) == 0 || c.request.Messages[0].Role != "system" {
		t.Fatal("missing system prompt")
	}
	var p prompt.Prompt
	if err := json.Unmarshal([]byte(c.request.Messages[0].Content.Text()), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfiguredPromptAndAssignmentStaySeparateAndCreationIsUnavailable(t *testing.T) {
	c, m := setup(t)
	if _, err := c.Send(c.Root(), "delegate"); err != nil {
		t.Fatal(err)
	}
	root := m.next(t)
	if got := systemPrompt(t, root).Role; got != "Coordinate work." {
		t.Fatal(got)
	}
	root.tool("create_test_agent", `{"input":{"task":"first task","context":"a quoted \"value\"","expected_output":"one line"}}`)
	var delegated call
	for range 2 {
		next := m.next(t)
		if next.request.Agent != c.Root() {
			delegated = next
		}
	}
	want := prompt.Prompt{Role: "Complete the assignment.", Instructions: []string{"Complete assigned work."}}
	if got := systemPrompt(t, delegated); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	incoming := delegated.request.Messages[1]
	wantAssignment := work.Work{Task: "first task", Context: `a quoted "value"`, ExpectedOutput: "one line"}
	if incoming.Envelope == nil || incoming.Envelope.Work == nil || *incoming.Envelope.Work != wantAssignment {
		t.Fatalf("lost assignment: %+v", incoming)
	}
	var wire message.Message
	if err := json.Unmarshal([]byte(incoming.Content.Text()), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Content != "" || wire.Work == nil || *wire.Work != wantAssignment {
		t.Fatalf("bad model payload: %+v", wire)
	}

	// The configured tool set controls both model visibility and dispatch.
	for _, definition := range delegated.request.Tools {
		if definition.Name == "create_test_agent" {
			t.Fatal("creation tool exposed to delegated agent")
		}
	}
	delegated.tool("create_test_agent", `{"input":{"task":"must not create another agent","context":null,"expected_output":null}}`)
	next := m.next(t)
	if next.request.Agent != delegated.request.Agent {
		t.Fatal("unexpected agent created")
	}
	last := next.request.Messages[len(next.request.Messages)-1]
	if last.Role != "tool" || last.Content.Text() != "Tool error: unknown tool: create_test_agent" {
		t.Fatalf("creation tool remained executable: %+v", last)
	}
	if len(c.Agents()) != 2 {
		t.Fatal("delegated agent created another agent")
	}

}

func TestCreateAgentStartsIdleAndHonorsExplicitSpec(t *testing.T) {
	c, m := setup(t)
	spec := agent.Spec{Provider: m, Prompt: prompt.Prompt{Role: "Custom agent", Instructions: []string{"Custom instruction"}}}
	created, err := c.CreateAgent(c.Root(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Prompt.Instructions[0] = "caller mutation"
	select {
	case call := <-m.calls:
		t.Fatalf("idle agent called provider: %+v", call.request)
	default:
	}
	receipt, err := c.Send(created.AgentID, "work")
	if err != nil {
		t.Fatal(err)
	}
	first := m.next(t)
	if got := systemPrompt(t, first); got.Role != "Custom agent" || got.Instructions[0] != "Custom instruction" {
		t.Fatal(got)
	}
	if env := first.request.Messages[1].Envelope; env.ID != receipt.MessageID || env.Content != "work" {
		t.Fatalf("bad initial message: %+v", env)
	}
}

func TestAssignmentEventsAndProviderSnapshotsAreIndependent(t *testing.T) {
	c, m := setup(t)
	if _, err := c.Send(c.Root(), "delegate"); err != nil {
		t.Fatal(err)
	}
	m.next(t).tool("create_test_agent", `{"input":{"task":"original","context":null,"expected_output":null}}`)
	observed := event(t, c, func(e conversation.Event) bool {
		msg, ok := e.(conversation.MessageEvent)
		return ok && msg.Message.Work != nil
	}).(conversation.MessageEvent)
	observed.Message.Work.Task = "event mutation"
	var delegated call
	for range 2 {
		next := m.next(t)
		if next.request.Agent != c.Root() {
			delegated = next
		} else {
			next.text("delegated")
		}
	}
	userReply(t, c, "delegated")
	if delegated.request.Messages[1].Envelope.Work.Task != "original" {
		t.Fatal("event mutated inbox payload")
	}
	delegated.request.Messages[1].Envelope.Work.Task = "provider mutation"
	delegated.text("done")
	m.next(t).text("reported")
	userReply(t, c, "reported")
	if _, err := c.Send(delegated.request.Agent, "follow-up"); err != nil {
		t.Fatal(err)
	}
	next := m.next(t)
	if next.request.Messages[1].Envelope.Work.Task != "original" {
		t.Fatal("provider mutated retained history")
	}
}

func TestApplicationSnapshotsCreationSpec(t *testing.T) {
	m := &controlledProvider{calls: make(chan call, 16)}
	c := conversation.New(context.Background())
	spec := agent.Spec{Provider: m, Prompt: prompt.Prompt{Role: "Execute", Instructions: []string{"original"}}, Tools: []tool.Tool{tool.SendMessage()}}
	create := creationTool(c, spec)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	spec.Prompt.Instructions[0] = "modified"
	spec.Tools[0] = nil
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: m, Prompt: prompt.Prompt{Role: "Coordinate"}, Tools: []tool.Tool{create}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(c.Root(), "delegate"); err != nil {
		t.Fatal(err)
	}
	m.next(t).tool("create_test_agent", `{"input":{"task":"work","context":null,"expected_output":null}}`)
	for range 2 {
		next := m.next(t)
		if next.request.Agent != c.Root() {
			if systemPrompt(t, next).Instructions[0] != "original" || len(next.request.Tools) != 1 || next.request.Tools[0].Name != "send_message" {
				t.Fatal("spec was not snapshotted")
			}
		}
	}
}

func TestInvalidAssignmentsDoNotCreateAgents(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `[]`, `{"input":{"task":" ","context":null,"expected_output":null}}`, `{"input":{"task":"ok","instructions":"replace prompt","context":null,"expected_output":null}}`, `{"instructions":"old","message":"old"}`, `{"input":{"task":"ok"}} {}`} {
		t.Run(raw, func(t *testing.T) {
			c, m := setup(t)
			if _, err := c.Send(c.Root(), "delegate"); err != nil {
				t.Fatal(err)
			}
			m.next(t).tool("create_test_agent", raw)
			next := m.next(t)
			last := next.request.Messages[len(next.request.Messages)-1]
			if last.Role != "tool" || !strings.HasPrefix(last.Content.Text(), "Tool error:") {
				t.Fatalf("invalid assignment accepted: %+v", last)
			}
			if len(c.Agents()) != 1 {
				t.Fatal("invalid assignment created an agent")
			}
		})
	}
}
