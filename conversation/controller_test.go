package conversation_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type answer struct {
	response provider.Response
	err      error
}
type call struct {
	request provider.Request
	answer  chan answer
}
type controlledProvider struct{ calls chan call }

func (m *controlledProvider) Submit(ctx context.Context, request provider.Request, observer provider.Observer) (provider.Response, error) {
	c := call{request: request, answer: make(chan answer, 1)}
	select {
	case m.calls <- c:
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
	select {
	case a := <-c.answer:
		return a.response, a.err
	case <-ctx.Done():
		return provider.Response{}, ctx.Err()
	}
}

func (m *controlledProvider) next(t *testing.T) call {
	t.Helper()
	select {
	case c := <-m.calls:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("no model call")
		return call{}
	}
}

func (c call) text(text string) { c.answer <- answer{response: provider.Response{Content: text}} }
func (c call) tool(name, arguments string) {
	c.answer <- answer{response: provider.Response{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: name, Arguments: json.RawMessage(arguments)}}}}
}

func setup(t *testing.T, tools ...tool.Tool) (*conversation.Controller, *controlledProvider) {
	t.Helper()
	m := &controlledProvider{calls: make(chan call, 16)}
	c := conversation.New(context.Background())
	executionSpec := agent.Spec{Provider: m, Prompt: prompt.Prompt{Role: "Complete the assignment.", Instructions: []string{"Complete assigned work."}}, Tools: append(append([]tool.Tool(nil), tools...), tool.SendMessage(), tool.MessageStatus(c.Receipt))}
	rootTools := append(append([]tool.Tool(nil), executionSpec.Tools...), creationTool(c, executionSpec))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	_, err := c.CreateAgent(message.User, agent.Spec{Provider: m, Prompt: prompt.Prompt{Role: "Coordinate work."}, Tools: rootTools})
	if err != nil {
		t.Fatal(err)
	}
	return c, m
}

func event(t *testing.T, c *conversation.Controller, wanted func(conversation.Event) bool) conversation.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		e, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if wanted(e) {
			return e
		}
	}
}

func userReply(t *testing.T, c *conversation.Controller, text string) message.Message {
	t.Helper()
	e := event(t, c, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.To == message.User && m.Message.Content == text
	})
	return e.(conversation.MessageEvent).Message
}

func TestRootPersistsAcrossMessagesAndAcknowledgesConsumption(t *testing.T) {
	c, m := setup(t)
	for _, text := range []string{"first", "second"} {
		receipt, err := c.Send(c.Root(), text)
		if err != nil || receipt.Status != message.Queued {
			t.Fatalf("%+v %v", receipt, err)
		}
		call := m.next(t)
		latest := call.request.Messages[len(call.request.Messages)-1]
		if latest.Envelope == nil || latest.Envelope.ID != receipt.MessageID {
			t.Fatal("input attribution lost")
		}
		ack, ok := c.Receipt(receipt.MessageID)
		if !ok || ack.Status != message.Consumed {
			t.Fatalf("not consumed: %+v", ack)
		}
		if text == "second" && len(call.request.Messages) != 4 {
			t.Fatalf("history was replaced: %+v", call.request.Messages)
		}
		call.text("reply " + text)
		result := userReply(t, c, "reply "+text)
		if result.ReplyTo != receipt.MessageID {
			t.Fatal("reply lost its message correlation")
		}
	}
	if agents := c.Agents(); len(agents) != 1 || agents[0].State.Terminal() {
		t.Fatalf("root did not persist: %+v", agents)
	}
}

func TestDelegationDoesNotBlockRootAndChildReplyReturnsThroughInbox(t *testing.T) {
	c, m := setup(t)
	_, _ = c.Send(c.Root(), "delegate this")
	m.next(t).tool("create_test_agent", `{"task":"child task","context":"Background","expected_output":"A result"}`)
	var root, child call
	for range 2 {
		got := m.next(t)
		if got.request.Agent == c.Root() {
			root = got
		} else {
			child = got
		}
	}
	if root.answer == nil || child.answer == nil {
		t.Fatal("delegation did not run both agents")
	}
	var creation struct {
		conversation.Creation
		Instruction *message.Receipt `json:"instruction"`
	}
	lastTool := root.request.Messages[len(root.request.Messages)-1]
	if err := json.Unmarshal([]byte(lastTool.Content.Text()), &creation); err != nil {
		t.Fatal(err)
	}
	if creation.AgentID != child.request.Agent || creation.Instruction == nil {
		t.Fatalf("creation lost its initial instruction receipt: %+v", creation)
	}
	if receipt, ok := c.Receipt(creation.Instruction.MessageID); !ok || receipt.Status != message.Consumed {
		t.Fatalf("initial instruction was not acknowledged: %+v", receipt)
	}
	root.text("delegated")
	userReply(t, c, "delegated")
	// Child's first Submit is deliberately still blocked here.
	_, _ = c.Send(c.Root(), "can we talk while it works?")
	active := m.next(t)
	if active.request.Agent != c.Root() {
		t.Fatal("wrong agent")
	}
	active.text("yes")
	userReply(t, c, "yes")
	child.text("child result")
	root = m.next(t)
	last := root.request.Messages[len(root.request.Messages)-1]
	if last.Envelope == nil || last.Envelope.From != child.request.Agent || last.Envelope.Kind != message.Reply {
		t.Fatalf("child response bypassed mailbox: %+v", last)
	}
	root.text("finished")
	userReply(t, c, "finished")
}

func TestSteeringQueuesDuringModelCallAndIsConsumedAfterToolBatch(t *testing.T) {
	echo := tool.Func[struct{}]{
		Spec:   tool.Definition[struct{}]{Name: "echo", Parameters: testParameters[struct{}](t)},
		Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) { return tool.Text("settled"), nil },
	}
	c, m := setup(t, echo)
	_, _ = c.Send(c.Root(), "begin")
	first := m.next(t)
	steer, err := c.Send(c.Root(), "change direction")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Receipt(steer.MessageID); got.Status != message.Queued {
		t.Fatalf("premature ack: %+v", got)
	}
	first.tool("echo", `{}`)
	next := m.next(t)
	history := next.request.Messages
	if history[len(history)-2].Role != "tool" || history[len(history)-2].Content.Text() != "settled" {
		t.Fatal("tool did not settle before steering")
	}
	if latest := history[len(history)-1]; latest.Envelope == nil || latest.Envelope.ID != steer.MessageID {
		t.Fatal("steering was not consumed")
	}
	if got, _ := c.Receipt(steer.MessageID); got.Status != message.Consumed {
		t.Fatal(got)
	}
	next.text("changed")
	userReply(t, c, "changed")
}

func TestSendMessageBindsSenderAndReportsReceipt(t *testing.T) {
	c, m := setup(t)
	created, err := c.CreateAgent(c.Root(), agent.Spec{Provider: m})
	child := created.AgentID
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Send(c.Root(), "send a message")
	args, _ := json.Marshal(map[string]string{"to": string(child), "message": "hello child"})
	m.next(t).tool("send_message", string(args))
	var root call
	for range 2 {
		next := m.next(t)
		if next.request.Agent == c.Root() {
			root = next
			continue
		}
		incoming := next.request.Messages[len(next.request.Messages)-1].Envelope
		if incoming.From != c.Root() || incoming.Content != "hello child" {
			t.Fatalf("bad envelope: %+v", incoming)
		}
		// Leave this model call pending; shutdown will cancel it.
	}
	last := root.request.Messages[len(root.request.Messages)-1]
	var receipt message.Receipt
	if err := json.Unmarshal([]byte(last.Content.Text()), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Recipient != child || receipt.Status != message.Queued {
		t.Fatal(receipt)
	}
	root.text("sent")
	userReply(t, c, "sent")
}

func TestStopReportsUnconsumedMessagesAndDoesNotStopOtherAgents(t *testing.T) {
	c, m := setup(t)
	created, err := c.CreateAgent(c.Root(), agent.Spec{Provider: m})
	child := created.AgentID
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(child, "work"); err != nil {
		t.Fatal(err)
	}
	_ = m.next(t) // Hold the child's model call.
	pending, _ := c.Send(child, "pending")
	if _, err := c.StopAgent(child); err != nil {
		t.Fatal(err)
	}
	event(t, c, func(e conversation.Event) bool {
		exit, ok := e.(conversation.AgentExited)
		return ok && exit.Agent == child
	})
	if got, _ := c.Receipt(pending.MessageID); got.Status != message.Undelivered {
		t.Fatal(got)
	}
	if _, err := c.Send(child, "too late"); err == nil {
		t.Fatal("stopped agent accepted input")
	}
	_, _ = c.Send(c.Root(), "still here?")
	m.next(t).text("here")
	userReply(t, c, "here")
}

func TestChildModelFailureIsRoutedToParent(t *testing.T) {
	c, m := setup(t)
	created, err := c.CreateAgent(c.Root(), agent.Spec{Provider: m})
	child := created.AgentID
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(child, "work"); err != nil {
		t.Fatal(err)
	}
	m.next(t).answer <- answer{err: errors.New("provider unavailable")}
	parent := m.next(t)
	last := parent.request.Messages[len(parent.request.Messages)-1].Envelope
	if last == nil || last.From != child || last.Kind != message.Failure {
		t.Fatalf("missing failure: %+v", last)
	}
	parent.text("worker failed")
	userReply(t, c, "worker failed")
}

func TestImmediateCloseJoinsAgentsAndRefusesFurtherWork(t *testing.T) {
	c, _ := setup(t)
	_, _ = c.Send(c.Root(), "possibly not yet consumed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(c.Root(), "late"); !errors.Is(err, conversation.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := c.CreateAgent(c.Root(), agent.Spec{}); !errors.Is(err, conversation.ErrClosed) {
		t.Fatal(err)
	}
	for _, a := range c.Agents() {
		if !a.State.Terminal() {
			t.Fatalf("unjoined agent: %+v", a)
		}
	}
}

func TestConversationHasExactlyOneRoot(t *testing.T) {
	c, m := setup(t)
	root := c.Root()
	agents := c.Agents()
	if root == "" || len(agents) != 1 || agents[0].ID != root || agents[0].Parent != message.User {
		t.Fatalf("invalid root: %q, agents: %+v", root, agents)
	}
	for _, parent := range []message.ActorID{message.User, "", "missing-agent"} {
		if _, err := c.CreateAgent(parent, agent.Spec{Provider: m}); err == nil {
			t.Fatalf("accepted invalid parent %q", parent)
		}
	}
	if len(c.Agents()) != 1 {
		t.Fatal("invalid creation registered an agent")
	}
	child, err := c.CreateAgent(root, agent.Spec{Provider: m})
	if err != nil {
		t.Fatal(err)
	}
	descendant, err := c.CreateAgent(child.AgentID, agent.Spec{Provider: m})
	if err != nil {
		t.Fatal(err)
	}
	agents = c.Agents()
	if len(agents) != 3 || agents[1].Parent != root || agents[2].ID != descendant.AgentID || agents[2].Parent != child.AgentID {
		t.Fatalf("invalid creation tree: %+v", agents)
	}
	if _, err := c.StopAgent(root); err != nil {
		t.Fatal(err)
	}
	event(t, c, func(e conversation.Event) bool {
		exited, ok := e.(conversation.AgentExited)
		return ok && exited.Agent == root
	})
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: m}); err == nil {
		t.Fatal("created a replacement root")
	}
	if _, err := c.CreateAgent(root, agent.Spec{Provider: m}); err == nil {
		t.Fatal("accepted a stopped parent")
	}
	if c.Root() != root || len(c.Agents()) != 3 {
		t.Fatal("root identity or registry changed")
	}
	for _, a := range c.Agents() {
		if a.ID != root && a.State.Terminal() {
			t.Fatal("stopping root stopped a descendant")
		}
	}
}

func testParameters[A any](t *testing.T) tool.Parameters[A] {
	t.Helper()
	p, err := tool.NewParameters[A]()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
