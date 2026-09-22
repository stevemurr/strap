package evalwire

import (
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eval"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/provider"
	"reflect"
	"testing"
	"time"
)

func TestConfigPreservesResolvedPolicy(t *testing.T) {
	cfg := harness.DefaultConfig()
	zero, off := 0.0, false
	cfg.Model.Generation.Temperature = &zero
	cfg.Model.Generation.EnableThinking = &off
	worker := cfg.Model
	worker.Model = "worker-model"
	cfg.Implementor.Model = &worker
	cfg.LSP = nil
	cfg.Root.Prompt.Instructions = append(cfg.Root.Prompt.Instructions, "Keep this user policy.")
	want := Config{Version: Version, Harness: cfg, Profile: "selected-profile"}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("resolved model/role/language policy changed during transport")
	}
	for _, bad := range []string{`{}`, `{"version":99}`, `{"version":1,"harness":{"model":{"backend":"vllm","timeout_ns":0}}}`} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Fatal("accepted incompatible config", bad)
		}
	}
}

func TestProgressPreservesToolFailuresAndUsage(t *testing.T) {
	now := time.Now().UTC()
	in, out := int64(10), int64(20)
	task := eval.Task{ID: "easy-01"}
	events := []conversation.Event{
		conversation.ContextTokensEvent{Agent: "worker", Revision: 7, Count: 4096},
		conversation.ToolEvent{Agent: "worker", Activity: agent.ToolActivity{Call: provider.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"input":{"path":"/workspace/missing.go"}}`)}, StartedAt: now, FinishedAt: now.Add(time.Second), Err: errors.New("file does not exist")}},
		conversation.UsageEvent{Agent: "worker", Observation: agent.UsageObservation{Usage: &provider.Usage{InputTokens: &in, OutputTokens: &out}}},
	}
	for _, event := range events {
		wire, emit, err := FromProgress(eval.Progress{Task: task, At: now, Event: event})
		if err != nil || !emit {
			t.Fatal(err)
		}
		b, _ := json.Marshal(wire)
		var restored Progress
		if err := json.Unmarshal(b, &restored); err != nil {
			t.Fatal(err)
		}
		got, err := restored.Decode(task)
		if err != nil {
			t.Fatal(err)
		}
		switch e := got.Event.(type) {
		case conversation.ToolEvent:
			if e.Activity.Err == nil || e.Activity.Err.Error() != "file does not exist" || e.Activity.Call.Name != "read_file" {
				t.Fatal(e)
			}
		case conversation.ContextTokensEvent:
			if e.Agent != "worker" || e.Revision != 7 || e.Count != 4096 {
				t.Fatal(e)
			}
		case conversation.UsageEvent:
			if *e.Observation.Usage.OutputTokens != 20 {
				t.Fatal(e)
			}
		default:
			t.Fatalf("lost event: %T", e)
		}
		if _, err := restored.Decode(eval.Task{ID: "another-task"}); err == nil {
			t.Fatal("cross-task progress accepted")
		}
	}
}
