package harness_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
)

// The shape every eval task ends in: the root has answered and every agent is
// parked on its inbox. Closing then is routine, so nothing in the recorded
// stream may claim a failure. A reader that has to filter cancellation to tell
// a finished task from a broken one cannot report either honestly.
func TestClosingIdleSessionRecordsNoCancellation(t *testing.T) {
	cfg := testConfig(t, false)
	cfg.Telemetry.ContextTokens = false
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: textResponse("done")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, err := s.Send(s.Root(), "hello"); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind == "output_finished" {
			break
		}
	}
	start := time.Now()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if settled := time.Since(start); settled > 2*time.Second {
		t.Errorf("an idle session took %s to close", settled.Round(time.Millisecond))
	}
	for {
		e, err := sub.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := s.ResolveRecord(ctx, e)
		if err != nil {
			continue
		}
		if text := string(resolved.Payload); strings.Contains(text, "context canceled") {
			t.Errorf("%s reported cancellation on a clean shutdown: %s", e.Kind, text)
		}
	}
}
