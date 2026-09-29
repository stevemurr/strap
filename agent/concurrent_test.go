package agent_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// slowTool takes 150ms and counts how many of its calls run at once.
func slowTool(t *testing.T, name string, running, peak *atomic.Int32) tool.Tool {
	params, err := tool.NewParameters[struct{}]()
	if err != nil {
		t.Fatal(err)
	}
	return tool.Func[struct{}]{Spec: tool.Definition[struct{}]{Name: name, Description: name + ".", Parameters: params}, Invoke: func(context.Context, tool.Call, struct{}) (tool.Result, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		running.Add(-1)
		return tool.Text(name), nil
	}}
}

// A batch of concurrent tools runs at once and settles in the order issued;
// a batch with any other tool runs one call at a time.
func TestConcurrentToolsRunTogetherAndSettleInOrder(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "all concurrent", true: "mixed"}[mixed], func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var running, peak, calls atomic.Int32
			c.Spec.Tools = []tool.Tool{slowTool(t, "open_url", &running, &peak), slowTool(t, "write_file", &running, &peak)}
			c.Spec.Concurrent = []string{"open_url"}
			second := "open_url"
			if mixed {
				second = "write_file"
			}
			order := make(chan []string, 1)
			c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
				if calls.Add(1) == 1 {
					return provider.Response{ToolCalls: []provider.ToolCall{
						{ID: "a", Name: "open_url", Arguments: json.RawMessage(`{"input":{}}`)},
						{ID: "b", Name: "open_url", Arguments: json.RawMessage(`{"input":{}}`)},
						{ID: "c", Name: second, Arguments: json.RawMessage(`{"input":{}}`)},
					}}, nil
				}
				var ids []string
				for _, m := range r.Messages {
					if m.Role == "tool" {
						ids = append(ids, m.ToolCallID)
					}
				}
				order <- ids
				return provider.Response{Content: "done"}, nil
			})
			var invocations []string
			c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
				if a, ok := e.(agent.ToolActivity); ok && a.FinishedAt.IsZero() {
					invocations = append(invocations, a.InvocationID+"="+a.Call.ID)
				}
				return nil
			})
			c.Outbox = senderFunc(func(context.Context, message.Draft) (message.Receipt, error) { return message.Receipt{}, nil })
			c.Inbox.Send(message.Message{ID: "start", Kind: message.Instruction, Content: "read"})
			a := mustAgent(t, c)
			done := make(chan error, 1)
			began := time.Now()
			go func() { done <- a.Run(ctx) }()
			ids := await(t, order)
			elapsed := time.Since(began)
			if len(ids) != 3 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
				t.Fatalf("results settled as %v", ids)
			}
			if len(invocations) != 3 || invocations[0] != "worker/tool-1=a" || invocations[2] != "worker/tool-3=c" {
				t.Fatalf("invocations %v", invocations)
			}
			if !mixed && (peak.Load() != 3 || elapsed > 400*time.Millisecond) {
				t.Fatalf("concurrent batch: peak %d, took %v", peak.Load(), elapsed)
			}
			if mixed && peak.Load() != 1 {
				t.Fatalf("a mixed batch ran %d calls at once", peak.Load())
			}
			cancel()
			await(t, done)
		})
	}
}
