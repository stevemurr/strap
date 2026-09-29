package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

type quietProvider struct{}

func (quietProvider) Submit(ctx context.Context, _ provider.Request, _ provider.Observer) (provider.Response, error) {
	<-ctx.Done()
	return provider.Response{}, ctx.Err()
}

// The route decides every delivery: the user's, a host's, and an agent's. Only
// a host notice an agent sends to itself is exempt.
func TestRouteGuardsEveryDelivery(t *testing.T) {
	denied := errors.New("no edge")
	var asked [][2]message.ActorID
	c := conversation.New(context.Background(), conversation.WithRoute(func(from, to message.ActorID, _ message.MessageKind) error {
		asked = append(asked, [2]message.ActorID{from, to})
		if from == message.User && to == "agent-1" || from == "agent-1" && to == "agent-2" {
			return nil
		}
		return denied
	}))
	// The agents block in their model calls until cancelled, so the close needs
	// a deadline to escalate from asking to cancelling.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Close(ctx)
	}()
	root, err := c.CreateAgent(message.User, agent.Spec{Provider: quietProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateAgent(root.AgentID, agent.Spec{Provider: quietProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(root.AgentID, "hello"); err != nil {
		t.Fatal("user to root:", err)
	}
	if _, err := c.Send(child.AgentID, "bypass"); !errors.Is(err, denied) {
		t.Fatal("user reached the child:", err)
	}
	if _, err := c.Deliver(root.AgentID, message.Draft{To: child.AgentID, Kind: message.Instruction, Content: "task"}); err != nil {
		t.Fatal("root to child:", err)
	}
	if _, err := c.Deliver(child.AgentID, message.Draft{To: root.AgentID, Kind: message.Instruction, Content: "up"}); !errors.Is(err, denied) {
		t.Fatal("unrouted host delivery:", err)
	}
	before := len(asked)
	if _, err := c.Deliver(child.AgentID, message.Draft{To: child.AgentID, Kind: message.Notification, Content: "note to self"}); err != nil {
		t.Fatal("host self-notice:", err)
	}
	if len(asked) != before {
		t.Fatal("host self-notice consulted the route")
	}
}
