package eval_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	if err != nil || len(results) != 1 || !results[0].Passed {
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
	if len(phases) != 5 || phases[0] != eval.Queued || phases[1] != eval.Starting || phases[2] != eval.Running || phases[3] != eval.Grading || phases[4] != eval.Finished {
		t.Fatal(phases)
	}
	if !tools || !usage || !tokens {
		t.Fatalf("tools=%v usage=%v tokens=%v", tools, usage, tokens)
	}
	if events[len(events)-1].Result == nil || !events[len(events)-1].Result.Passed {
		t.Fatal("completion preceded observation drain")
	}
	events = nil
	if _, err := eval.Run(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Phase != eval.Finished || !events[1].Reused {
		t.Fatal(events)
	}
}

func TestProgressPreservesTierBarriers(t *testing.T) {
	ladder := writeLadder(t)
	source := filepath.Join(ladder, "easy", "00-probe")
	for _, name := range []string{"easy/01-probe", "medium/00-probe"} {
		dir := filepath.Join(ladder, name)
		if err := os.CopyFS(dir, os.DirFS(source)); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "task.json"))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), "easy-00-probe", strings.ReplaceAll(name, "/", "-")))
		if strings.HasPrefix(name, "medium") {
			data = []byte(strings.ReplaceAll(string(data), `"tier":"easy"`, `"tier":"medium"`))
		}
		if err := os.WriteFile(filepath.Join(dir, "task.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts := options(t, ladder, &script{})
	opts.Parallel = 2
	var mu sync.Mutex
	finishedEasy := 0
	violation := false
	opts.Observe = func(p eval.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if p.Phase == eval.Finished && p.Task.Tier == "easy" {
			finishedEasy++
		}
		if p.Phase == eval.Starting && p.Task.Tier == "medium" && finishedEasy != 2 {
			violation = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	results, err := eval.Run(ctx, opts)
	if err != nil || len(results) != 3 || violation || finishedEasy != 2 {
		t.Fatal(len(results), err, violation, finishedEasy)
	}
}
