package replay_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/replay"
	"github.com/stevemurr/strap/provider"
)

// alternating answers with a real tool call, then the call written as text.
type alternating struct{ n atomic.Int32 }

func (a *alternating) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if a.n.Add(1)%2 == 1 {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "c", Name: "list_agents", Arguments: []byte(`{"input":{}}`)}}}, nil
	}
	return provider.Response{Content: `list_agents()`}, nil
}

// A probe replays to the chosen call, takes the request the harness built
// under each variant, and tallies what the model did with it.
func TestProbeSamplesTheRecordedRequest(t *testing.T) {
	rec := open(t, record(t))
	var models []harness.ModelConfig
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	results, err := replay.Probe(ctx, rec, replay.ProbeOptions{
		Agent: "agent-1", Call: 2, Samples: 6, Model: &harness.ModelConfig{Backend: "vllm", Model: "m"},
		Variants: []replay.Variant{
			{Name: "recorded"},
			{Name: "cooler", Model: func(m *harness.ModelConfig) { v := 0.5; m.Generation.Temperature = &v }},
			{Name: "reworded", Configure: func(c *harness.Config) { c.Manager.Prompt.Role = "You manage." }},
		},
		Sampler: func(m harness.ModelConfig) (provider.Provider, error) {
			models = append(models, m)
			return &alternating{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || !results[0].Recorded || !results[1].Recorded || results[2].Recorded {
		t.Fatalf("recorded requests: %v %v %v", results[0].Recorded, results[1].Recorded, results[2].Recorded)
	}
	if got := results[0].Counts(); len(got) != 2 || got[0] != (replay.OutcomeCount{Outcome: "call list_agents", Count: 3}) || got[1] != (replay.OutcomeCount{Outcome: "text list_agents", Count: 3}) {
		t.Fatalf("counts %v", got)
	}
	if models[1].Generation.Temperature == nil || *models[1].Generation.Temperature != 0.5 || models[0].Generation.Temperature != nil {
		t.Fatalf("variant models %+v", models)
	}
	if n := len(results[0].Request.Messages); n == 0 || results[0].Request.Agent != "agent-1" {
		t.Fatalf("request %+v", results[0].Request)
	}
	if _, err := replay.Probe(ctx, rec, replay.ProbeOptions{Agent: "agent-1", Call: 99}); err == nil {
		t.Fatal("probed a call the recording never made")
	}
}

// A trace the current harness no longer reproduces is refused: its earlier
// turns were made under other prompts or tools.
func TestProbeRefusesAStaleTrace(t *testing.T) {
	rec := open(t, record(t))
	// An older harness's manager prompt, as the recorded history shows it.
	for i, m := range rec.Histories["agent-1"] {
		if m.Role == "system" {
			rec.Histories["agent-1"][i].Content = content.Text(m.Content.Text() + " stale")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	opts := replay.ProbeOptions{Agent: "agent-1", Call: 2, Samples: 1, Model: &harness.ModelConfig{Backend: "vllm", Model: "m"},
		Sampler: func(harness.ModelConfig) (provider.Provider, error) { return &alternating{}, nil }}
	if _, err := replay.Probe(ctx, rec, opts); !errors.Is(err, replay.ErrStale) {
		t.Fatalf("probed a stale trace: %v", err)
	}
	opts.AllowStale = true
	if _, err := replay.Probe(ctx, rec, opts); err != nil {
		t.Fatal(err)
	}
}

// An After variant probes the call that follows the recorded one: the
// request carries the recorded response and the messages After supplies.
func TestProbeAfterFollowsTheRecordedCall(t *testing.T) {
	rec := open(t, record(t))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var followed provider.Message
	results, err := replay.Probe(ctx, rec, replay.ProbeOptions{
		Agent: "agent-1", Call: 2, Samples: 1, Model: &harness.ModelConfig{Backend: "vllm", Model: "m"},
		Variants: []replay.Variant{{Name: "recorded"}, {Name: "refused", After: func(m provider.Message) []provider.Message {
			followed = m
			return []provider.Message{{Role: "tool", Content: content.Text("Tool error: refused"), ToolCallID: "x"}}
		}}},
		Sampler: func(harness.ModelConfig) (provider.Provider, error) { return &alternating{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	base, after := results[0].Request.Messages, results[1].Request.Messages
	if results[1].Recorded || len(after) != len(base)+2 {
		t.Fatalf("followed request has %d messages after %d (recorded %v)", len(after), len(base), results[1].Recorded)
	}
	if got := after[len(base)]; got.Role != "assistant" || got.Content.Text() != followed.Content.Text() || len(got.ToolCalls) != len(followed.ToolCalls) {
		t.Fatalf("recorded response %+v, followed %+v", got, followed)
	}
	if got := after[len(after)-1]; got.Role != "tool" || got.Content.Text() != "Tool error: refused" {
		t.Fatalf("supplied message %+v", got)
	}
}
