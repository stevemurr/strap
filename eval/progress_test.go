package eval_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
)

func TestProgressObservesExecutionAndFinalTelemetry(t *testing.T) {
	opts := options(t, writeLadder(t), &script{write: true, content: "package probe\nfunc Answer() int { return 42 }\n"})
	var mu sync.Mutex
	var events []eval.Progress
	opts.Observe = func(p eval.Progress) { mu.Lock(); defer mu.Unlock(); events = append(events, p) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	results, err := eval.Run(ctx, opts)
	if err != nil || len(results) != 1 || results[0].Outcome != eval.Submitted {
		t.Fatal(results, err)
	}
	var phases []eval.Phase
	tools, usage, tokens := false, false, false
	for _, e := range events {
		if e.Phase != "" {
			phases = append(phases, e.Phase)
		}
		switch e.Event.(type) {
		case conversation.ToolEvent:
			tools = true
		case conversation.UsageEvent:
			usage = true
		case conversation.ContextTokensEvent:
			tokens = true
		}
	}
	if len(phases) != 4 || phases[0] != eval.Queued || phases[1] != eval.Starting || phases[2] != eval.Running || phases[3] != eval.Finished {
		t.Fatal(phases)
	}
	if !tools || !usage || !tokens {
		t.Fatalf("tools=%v usage=%v tokens=%v", tools, usage, tokens)
	}
	if events[len(events)-1].Result == nil || events[len(events)-1].Result.Outcome != eval.Submitted {
		t.Fatal("completion preceded observation drain")
	}
}
