package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// stalls streams reasoning until the agent cancels the call, for the first n
// calls, then answers. It mirrors a model that deliberates without acting.
func stalls(n int, calls *atomic.Int32) streamFunc {
	return func(ctx context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
		if int(calls.Add(1)) > n {
			return provider.Response{Content: "acted"}, nil
		}
		for {
			if err := o.OnDelta(provider.Delta{Channel: provider.ChannelReasoning, Text: strings.Repeat("Let me just do it. ", 32)}); err != nil {
				return provider.Response{}, err
			}
		}
	}
}

func TestReasoningOverrunIsCutOffAndRetriedOnce(t *testing.T) {
	c := config()
	c.Spec.ReasoningLimit = 4096
	var calls atomic.Int32
	c.Spec.Provider = stalls(1, &calls)
	var mu sync.Mutex
	var finished []agent.OutputFinished
	var notices []string
	c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
		mu.Lock()
		defer mu.Unlock()
		switch v := e.(type) {
		case agent.OutputFinished:
			finished = append(finished, v)
		case agent.HistoryAppended:
			if v.Message.Role == "user" && strings.Contains(v.Message.Content.Text(), "cut off") {
				notices = append(notices, v.Message.Content.Text())
			}
		}
		return nil
	})
	replies := make(chan message.Draft, 1)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	if reply := await(t, replies); reply.Content != "acted" {
		t.Fatal(reply)
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 2 || len(finished) != 2 || len(notices) != 1 {
		t.Fatalf("calls %d finished %d notices %d", calls.Load(), len(finished), len(notices))
	}
	first := finished[0]
	// Published reasoning stops at the last chunk that fit under the limit.
	if first.Status != agent.OutputFailed || !errors.Is(first.Err, agent.ErrReasoningLimit) || first.ReasoningBytes == 0 || first.ReasoningBytes > 4096 || first.HistoryPosition != nil {
		t.Fatalf("%+v", first)
	}
	if finished[1].Status != agent.OutputComplete {
		t.Fatalf("%+v", finished[1])
	}
}

func TestSecondReasoningOverrunEndsTheAgent(t *testing.T) {
	c := config()
	c.Spec.ReasoningLimit = 4096
	var calls atomic.Int32
	c.Spec.Provider = stalls(2, &calls)
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	err := mustAgent(t, c).Run(context.Background())
	if !errors.Is(err, agent.ErrReasoningLimit) || calls.Load() != 2 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
}

func TestZeroReasoningLimitIsUnlimited(t *testing.T) {
	c := config()
	var calls atomic.Int32
	c.Spec.Provider = streamFunc(func(_ context.Context, _ provider.Request, o provider.Observer) (provider.Response, error) {
		calls.Add(1)
		for i := 0; i < 64; i++ {
			if err := o.OnDelta(provider.Delta{Channel: provider.ChannelReasoning, Text: strings.Repeat("x", 1024)}); err != nil {
				return provider.Response{}, err
			}
		}
		return provider.Response{Content: "acted", Reasoning: strings.Repeat("x", 64*1024)}, nil
	})
	replies := make(chan message.Draft, 1)
	c.Outbox = senderFunc(func(_ context.Context, d message.Draft) (message.Receipt, error) {
		replies <- d
		return message.Receipt{}, nil
	})
	_ = c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "begin"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mustAgent(t, c).Run(ctx) }()
	if reply := await(t, replies); reply.Content != "acted" || calls.Load() != 1 {
		t.Fatal(reply, calls.Load())
	}
	cancel()
	await(t, done)
}
