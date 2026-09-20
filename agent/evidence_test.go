package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"sync/atomic"
	"testing"
	"time"
)

type evidenceTool struct{}

func (t evidenceTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "diagnostic", Parameters: t.InputContract().Schema()}
}
func (evidenceTool) Call(_ context.Context, c tool.Call) (tool.Result, error) {
	if c.InvocationID == "" {
		panic("missing host invocation")
	}
	r, err := tool.ExecutionResult(tool.Text("partial result"), &tool.ExecutionBinding{EvidenceRef: tool.NewExecutionEvidenceRef(), Actor: c.Actor}, errors.New("diagnostic failed"))
	return r, errors.Join(err, errors.New("diagnostic failed"))
}
func TestEvidenceReceiptRequiresAcceptedFinishAndSurvivesToolError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonfatal-error", true: "publication-failure"}[fail], func(t *testing.T) {
			c := config()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var calls atomic.Int32
			c.Spec.Tools = []tool.Tool{evidenceTool{}}
			c.Spec.Provider = modelFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
				if calls.Add(1) == 1 {
					return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "diagnostic", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
				}
				var result map[string]any
				if err := json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Content.Text()), &result); err != nil || result["evidence_ref"] == nil || result["error"] == nil {
					t.Error("lost error evidence", result, err)
				}
				cancel()
				return provider.Response{Content: "done"}, nil
			})
			c.Reporter = agent.ReporterFunc(func(_ context.Context, e agent.Event) error {
				if activity, ok := e.(agent.ToolActivity); ok && !activity.FinishedAt.IsZero() && fail {
					return errors.New("record rejected")
				}
				return nil
			})
			c.Inbox.Send(message.Message{ID: "go", Kind: message.Instruction, Content: "inspect"})
			a := mustAgent(t, c)
			err := a.Run(ctx)
			if fail {
				if err == nil || calls.Load() != 1 {
					t.Fatal(err, calls.Load())
				}
			} else if calls.Load() != 2 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}

func (t evidenceTool) InputContract() tool.Contract {
	p, err := tool.NewParameters[struct{}]()
	if err != nil {
		panic(err)
	}
	return p.Contract()
}
