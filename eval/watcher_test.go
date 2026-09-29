package eval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/eventlog"
)

// Server metrics samples keep arriving while every agent sits idle; they must
// not reset the idle clock, or a stalled session runs to its whole budget.
func TestIdleIgnoresServerMetricsSamples(t *testing.T) {
	store, err := eventlog.NewMemory("session-1", eventlog.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.New(store, eventlog.Limits{Entries: 64, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub, err := log.Subscribe(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				_ = log.Publish(eventlog.Data{Kind: "server_metrics", Time: now, Payload: json.RawMessage(`{"phase":"sample"}`)})
			}
		}
	}()
	w := watcher{}
	start := time.Now()
	err = w.wait(ctx, nil, sub, 5*time.Second, time.Second, 200*time.Millisecond, t.Logf)
	if !errors.Is(err, errNoReply) || time.Since(start) > 2*time.Second {
		t.Fatalf("wait = %v after %s, want errNoReply after the 200ms idle limit", err, time.Since(start))
	}
}
