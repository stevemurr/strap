package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func malformedCall() error {
	return fmt.Errorf("vllm: %w", &provider.ToolArgumentsError{CallID: "bad", Name: "write_file", Arguments: `{"path":"a","content":"open`, FinishReason: "tool_calls", Calls: 1})
}

// A tool call whose arguments never became JSON was not dispatched and did not
// enter history, so the agent asks the model again instead of exiting.
func TestMalformedToolCallIsRegeneratedWithANotice(t *testing.T) {
	c := config()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drafts := make(chan message.Draft, 1)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		drafts <- d
		cancel()
		return message.Receipt{}, nil
	})
	calls := 0
	c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
		calls++
		if calls <= 2 {
			return provider.Response{}, malformedCall()
		}
		last := r.Messages[len(r.Messages)-1]
		if len(r.Messages) != 4 || last.Role != "user" || !strings.Contains(last.Content.Text(), "write_file call was discarded") || !strings.Contains(last.Content.Text(), "incomplete JSON") {
			t.Errorf("history after rejections: %+v", r.Messages)
		}
		for _, m := range r.Messages {
			if m.Role == "assistant" {
				t.Errorf("rejected call entered history: %+v", m)
			}
		}
		return provider.Response{Content: "answer"}, nil
	})
	a := mustAgent(t, c)
	c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "go"})
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	if d := await(t, drafts); d.Content != "answer" {
		t.Fatal(d)
	}
}

// Persistent malformed output still ends the agent, with the rejected call preserved.
func TestPersistentlyMalformedToolCallsStillFail(t *testing.T) {
	c := config()
	calls := 0
	c.Spec.Provider = modelFunc(func(context.Context, provider.Request) (provider.Response, error) {
		calls++
		return provider.Response{}, malformedCall()
	})
	a := mustAgent(t, c)
	c.Inbox.Send(message.Message{ID: "input", Kind: message.Instruction, Content: "go"})
	err := a.Run(context.Background())
	var detail *provider.ToolArgumentsError
	if !errors.As(err, &detail) || detail.CallID != "bad" {
		t.Fatalf("lost rejection: %v", err)
	}
	if calls != 3 || a.State() != agent.Failed {
		t.Fatal(calls, a.State())
	}
}
