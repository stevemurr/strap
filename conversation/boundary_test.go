package conversation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/tool"
)

func emptyConversation(t *testing.T) *conversation.Controller {
	t.Helper()
	c := conversation.New(context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestBootstrapAndNoImplicitTools(t *testing.T) {
	c := emptyConversation(t)
	if c.Root() != "" || len(c.Agents()) != 0 {
		t.Fatal("new conversation is not empty")
	}
	if _, err := c.CreateAgent(message.User, agent.Spec{}); err == nil {
		t.Fatal("missing provider accepted")
	}
	if c.Root() != "" || len(c.Agents()) != 0 {
		t.Fatal("failed creation claimed root")
	}
	m := &controlledProvider{calls: make(chan call, 16)}
	root, err := c.CreateAgent(message.User, agent.Spec{Provider: m})
	if err != nil {
		t.Fatal(err)
	}
	if c.Root() != root.AgentID {
		t.Fatal("root not established")
	}
	child, err := c.CreateAgent(root.AgentID, agent.Spec{Provider: m})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []message.ActorID{root.AgentID, child.AgentID} {
		if _, err := c.Send(id, "hello"); err != nil {
			t.Fatal(err)
		}
		request := m.next(t)
		if len(request.request.Tools) != 0 {
			t.Fatalf("injected tools: %+v", request.request.Tools)
		}
		request.tool("create_test_agent", `{"task":"unexpected"}`)
		next := m.next(t)
		last := next.request.Messages[len(next.request.Messages)-1]
		if last.Content.Text() != "Tool error: unknown tool: create_test_agent" {
			t.Fatalf("unexpected dispatch: %+v", last)
		}
	}
	if len(c.Agents()) != 2 {
		t.Fatal("unconfigured tool created an agent")
	}
}

func TestSharedCreationToolUsesExecutingAgentAndConfiguredSpec(t *testing.T) {
	c := emptyConversation(t)
	callers := &controlledProvider{calls: make(chan call, 16)}
	workers := &controlledProvider{calls: make(chan call, 16)}
	create := creationTool(c, agent.Spec{Provider: workers, Prompt: prompt.Prompt{Role: "Configured execution prompt"}})
	spec := agent.Spec{Provider: callers, Prompt: prompt.Prompt{Role: "Calling agent"}, Tools: []tool.Tool{create, tool.SendMessage()}}
	root, err := c.CreateAgent(message.User, spec)
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateAgent(root.AgentID, spec)
	if err != nil {
		t.Fatal(err)
	}
	// The exact same creation tool is invoked from two independent agent loops.
	for _, id := range []message.ActorID{root.AgentID, child.AgentID} {
		if _, err := c.Send(id, "delegate"); err != nil {
			t.Fatal(err)
		}
		callers.next(t).tool("create_test_agent", `{"task":"work"}`)
		worker := workers.next(t)
		if systemPrompt(t, worker).Role != "Configured execution prompt" || len(worker.request.Tools) != 0 {
			t.Fatal("caller configuration leaked into created agent")
		}
		env := worker.request.Messages[1].Envelope
		if env.From != id || env.Work == nil || env.Work.Task != "work" {
			t.Fatalf("wrong assignment sender: %+v", env)
		}
		agents := c.Agents()
		newest := agents[len(agents)-1]
		if newest.ID != worker.request.Agent || newest.Parent != id {
			t.Fatalf("wrong creation parent: %+v", newest)
		}
		continuation := callers.next(t)
		if continuation.request.Agent != id {
			t.Fatal("tool returned to wrong caller")
		}
		var result struct {
			conversation.Creation
			Instruction message.Receipt `json:"instruction"`
		}
		last := continuation.request.Messages[len(continuation.request.Messages)-1]
		if err := json.Unmarshal([]byte(last.Content.Text()), &result); err != nil {
			t.Fatal(err)
		}
		if result.AgentID != newest.ID || result.Instruction.MessageID != env.ID {
			t.Fatal("creation receipt does not match delivery")
		}
		// Sender attribution is also supplied per invocation for the shared send tool.
		continuation.tool("send_message", `{"to":"user","message":"progress","actor":"forged"}`)
		continuation = callers.next(t)
		rejected := continuation.request.Messages[len(continuation.request.Messages)-1].Content.Text()
		if !strings.Contains(rejected, "actor is not an allowed field") {
			t.Fatalf("forged identity was not rejected: %s", rejected)
		}
		continuation.tool("send_message", `{"to":"user","message":"progress"}`)
		observed := event(t, c, func(e conversation.Event) bool {
			msg, ok := e.(conversation.MessageEvent)
			return ok && msg.Message.Content == "progress"
		}).(conversation.MessageEvent)
		if observed.Message.From != id {
			t.Fatal("model arguments changed sender identity")
		}
		callers.next(t) // Hold each caller so only the next explicitly messaged agent runs.
	}
}

func TestConcurrentRootCreationAndClose(t *testing.T) {
	c := emptyConversation(t)
	m := &controlledProvider{calls: make(chan call, 16)}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = c.CreateAgent(message.User, agent.Spec{Provider: m})
			_ = c.Root()
		}()
	}
	close(start)
	wg.Wait()
	if len(c.Agents()) != 1 {
		t.Fatal("concurrent creation did not establish exactly one root")
	}
	// Exercise admission against Close for an already established conversation.
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.CreateAgent(c.Root(), agent.Spec{Provider: m}) }()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := c.CreateAgent(c.Root(), agent.Spec{Provider: m}); !errors.Is(err, conversation.ErrClosed) {
		t.Fatalf("creation after close: %v", err)
	}
}

func TestEmptyConversationCloses(t *testing.T) {
	c := emptyConversation(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextEvent(ctx); !errors.Is(err, inbox.ErrClosed) {
		t.Fatalf("event stream after close: %v", err)
	}
}
