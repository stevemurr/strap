package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// waitScript finishes a task by calling wait_for_input alongside its summary,
// the way nemotron-lightning ended seven ladder tasks, then replies with text
// once the wait is rejected.
type waitScript struct{ calls atomic.Int32 }

func (p *waitScript) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) == 1 {
		return provider.Response{Content: "All done, summary follows.", ToolCalls: []provider.ToolCall{{ID: "w", Name: "wait_for_input", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	return provider.Response{Content: "Final reply."}, nil
}

func TestRootWaitWithoutActiveWorkIsRejectedAndReplyFollows(t *testing.T) {
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	p := &waitScript{}
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err := s.Send(s.Root(), "finish the task"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rejected, replied := false, false
	for !(rejected && replied) {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err, rejected, replied)
		}
		switch v := e.(type) {
		case conversation.ToolEvent:
			if v.Activity.Call.Name == "wait_for_input" && !v.Activity.FinishedAt.IsZero() {
				if v.Activity.Err == nil || !strings.Contains(v.Activity.Err.Error(), "rejected") {
					t.Fatalf("wait without work must be rejected: %v", v.Activity.Err)
				}
				rejected = true
			}
		case conversation.MessageEvent:
			if v.Message.From == s.Root() && v.Message.To == message.User && v.Message.Kind == message.Reply {
				if v.Message.Content != "Final reply." {
					t.Fatalf("reply %q", v.Message.Content)
				}
				replied = true
			}
		}
	}
	if p.calls.Load() != 2 {
		t.Fatal("calls", p.calls.Load())
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
