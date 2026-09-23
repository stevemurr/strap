package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func sees(r provider.Request, id string) bool {
	for _, m := range r.Messages {
		if strings.Contains(m.Content.Text(), `"id":"`+id+`"`) {
			return true
		}
	}
	return false
}

// The easy-08 sequence: a second assignment reaches a worker that is busy with
// the first. It must not be consumed into the running exchange, where a reply
// about the first work would leave it unanswered; it starts the next exchange.
func TestAssignmentArrivingMidExchangeStartsTheNextExchange(t *testing.T) {
	c := config()
	second := message.Message{ID: "second", Kind: message.Instruction, Work: &work.Work{ID: "work-2", Task: "run go vet"}}
	var calls atomic.Int32
	var mistakes atomic.Int32
	c.Spec.Tools = []tool.Tool{customTool{name: "step"}}
	c.Spec.Provider = streamFunc(func(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
		switch calls.Add(1) {
		case 1: // Working on the first assignment when the second arrives.
			_ = c.Inbox.Send(second)
			return provider.Response{ToolCalls: []provider.ToolCall{{ID: "s", Name: "step", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
		case 2:
			if sees(r, "second") {
				mistakes.Add(1)
			}
			return provider.Response{Content: "first done"}, nil
		default:
			if !sees(r, "second") {
				mistakes.Add(1)
			}
			return provider.Response{Content: "second done"}, nil
		}
	})
	replies := make(chan message.Draft, 2)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "first", Kind: message.Instruction, Work: &work.Work{ID: "work-1", Task: "implement"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	for _, want := range []struct{ to, content string }{{"first", "first done"}, {"second", "second done"}} {
		if r := await(t, replies); string(r.ReplyTo) != want.to || r.Content != want.content {
			t.Fatalf("reply %+v, want %s to %s", r, want.content, want.to)
		}
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if mistakes.Load() != 0 || calls.Load() != 3 {
		t.Fatalf("second assignment seen in the wrong exchange %d times; %d calls", mistakes.Load(), calls.Load())
	}
}
