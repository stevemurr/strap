package conversation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type idleProvider struct{ calls atomic.Int32 }

func (p *idleProvider) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	p.calls.Add(1)
	return provider.Response{Content: "unexpected call"}, nil
}

type cancelOnDefinition struct{ cancel context.CancelFunc }

func (t cancelOnDefinition) Definition() provider.ToolDefinition {
	t.cancel()
	return provider.ToolDefinition{Name: "cancel_on_definition", Parameters: t.InputContract().Schema()}
}
func (cancelOnDefinition) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, nil
}

func TestAssignmentDeliveryFailureStopsNewAgent(t *testing.T) {
	p := &idleProvider{}
	c := emptyConversation(t)
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "Coordinate"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancellation happens while the new agent is being configured, after the
	// tool's context check but before it sends the assignment.
	spec := agent.Spec{Provider: p, Tools: []tool.Tool{cancelOnDefinition{cancel}}}
	create := creationTool(c, spec)
	_, err := create.Call(ctx, tool.Call{Actor: c.Root(), Sender: canceledSender{}, Arguments: json.RawMessage(`{"input":{"context":null,"expected_output":null,"task":"work"}}`)})
	agents := c.Agents()
	if len(agents) != 2 {
		t.Fatalf("expected creation before delivery failure: %+v", agents)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), string(agents[1].ID)) || !strings.Contains(err.Error(), "delivery failed") {
		t.Fatalf("missing partial outcome: %v", err)
	}
	wait, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	for {
		event, err := c.NextEvent(wait)
		if err != nil {
			t.Fatal(err)
		}
		if msg, ok := event.(conversation.MessageEvent); ok && msg.Message.Work != nil {
			t.Fatal("assignment delivered after cancellation")
		}
		if exit, ok := event.(conversation.AgentExited); ok && exit.Agent == agents[1].ID {
			break
		}
	}
	if p.calls.Load() != 0 {
		t.Fatal("an idle agent called the provider")
	}
	if err := c.Close(wait); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := c.NextEvent(wait)
		if errors.Is(err, inbox.ErrClosed) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

// This sender models cancellation between creation and assignment delivery.
type canceledSender struct{}

func (canceledSender) Send(ctx context.Context, _ message.Draft) (message.Receipt, error) {
	return message.Receipt{}, ctx.Err()
}

// creationTool is application wiring used by the conversation integration tests.
// The library tool knows only the callback, never the controller or agent spec.
func creationTool(c *conversation.Controller, spec agent.Spec) tool.Tool {
	spec = spec.Clone()
	type args struct {
		Task           string  `json:"task"`
		Context        *string `json:"context"`
		ExpectedOutput *string `json:"expected_output"`
	}
	params, err := tool.NewParameters[args](tool.MinLength("task", 1), tool.Nullable("context", "no context"), tool.Nullable("expected_output", "no output requirement"))
	if err != nil {
		panic(err)
	}
	return tool.Func[args]{Spec: tool.Definition[args]{Name: "create_test_agent", Parameters: params}, Invoke: func(ctx context.Context, call tool.Call, a args) (tool.Result, error) {
		if strings.TrimSpace(a.Task) == "" {
			return tool.Result{}, errors.New("task is required")
		}
		assignment := work.Work{Task: a.Task}
		if a.Context != nil {
			assignment.Context = *a.Context
		}
		if a.ExpectedOutput != nil {
			assignment.ExpectedOutput = *a.ExpectedOutput
		}
		created, err := c.CreateAgent(call.Actor, spec)
		if err != nil {
			return tool.Result{}, err
		}
		receipt, err := call.Sender.Send(ctx, message.Draft{
			To:   created.AgentID,
			Kind: message.Instruction,
			Work: &assignment,
		})
		if err != nil {
			_, stopErr := c.StopAgent(created.AgentID)
			return tool.Result{}, fmt.Errorf("agent %s created but assignment delivery failed; stop requested: %w", created.AgentID, errors.Join(err, stopErr))
		}
		encoded, err := json.Marshal(struct {
			conversation.Creation
			Instruction message.Receipt `json:"instruction"`
		}{Creation: created, Instruction: receipt})
		return tool.Text(string(encoded)), err
	}}
}

func TestInvalidTypedToolRejectedBeforeAgentRegistration(t *testing.T) {
	c := emptyConversation(t)
	p := &idleProvider{}
	invalid := tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Name: "invalid"}, Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) {
		t.Fatal("invalid tool invoked")
		return tool.Result{}, nil
	}}
	if _, err := c.CreateAgent(message.User, agent.Spec{Provider: p, Tools: []tool.Tool{invalid}}); err == nil || !strings.Contains(err.Error(), "uninitialized parameters") {
		t.Fatal(err)
	}
	if len(c.Agents()) != 0 || p.calls.Load() != 0 {
		t.Fatal("invalid tool registered")
	}
}

func (t cancelOnDefinition) InputContract() tool.Contract {
	p, err := tool.NewParameters[struct{}]()
	if err != nil {
		panic(err)
	}
	return p.Contract()
}
