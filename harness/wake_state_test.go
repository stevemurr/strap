package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type wakeState struct {
	rootCalls, workerCalls atomic.Int32
	done                   chan error
	plan                   work.Plan
	w                      work.Work
}

func wakeBlock(r provider.Request) *work.ActorState {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if m := r.Messages[i]; m.Envelope != nil && m.Envelope.State != nil {
			return m.Envelope.State
		}
	}
	return nil
}

type wakeRoot struct{ p *wakeState }

func (f wakeRoot) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	p := f.p
	switch p.rootCalls.Add(1) {
	case 1:
		// The first exchange has no plan or work yet: no state block at all.
		if wakeBlock(r) != nil {
			return provider.Response{}, fmt.Errorf("state block before any state: %+v", wakeBlock(r))
		}
		return operation("create_agent", map[string]any{"role": "implementor"})
	case 2:
		return operation("create_plan", map[string]any{"title": "Wake", "steps": []any{map[string]any{"title": "first", "acceptance_criteria": nil}, map[string]any{"title": "second", "acceptance_criteria": nil}}})
	case 3:
		if err := json.Unmarshal([]byte(lastResult(r)), &p.plan); err != nil {
			return provider.Response{}, err
		}
		var created struct {
			AgentID identity.ActorID `json:"agent_id"`
		}
		for i := len(r.Messages) - 1; i >= 0; i-- {
			if r.Messages[i].Role == "tool" && json.Unmarshal([]byte(r.Messages[i].Content.Text()), &created) == nil && created.AgentID != "" {
				break
			}
		}
		return operation("assign_implementation", map[string]any{"assignee": created.AgentID, "task": "do first", "scope": map[string]any{"plan_id": p.plan.ID, "step_ids": []work.StepID{p.plan.Steps[0].ID}}, "context": nil, "expected_output": nil})
	case 4:
		if err := json.Unmarshal([]byte(lastResult(r)), &p.w); err != nil {
			return provider.Response{}, err
		}
		return operation("wait_for_input", struct{}{})
	default:
		// Woken by the worker's message: the block names the plan, the step
		// reservation and the owned work, all from the store.
		state := wakeBlock(r)
		var err error
		switch {
		case state == nil:
			err = fmt.Errorf("root woke without a state block")
		case len(state.Plans) != 1 || state.Plans[0].PlanID != p.plan.ID || len(state.Plans[0].Steps) != 2:
			err = fmt.Errorf("plan state: %+v", state.Plans)
		case state.Plans[0].Steps[0].ReservedBy != p.w.ID || state.Plans[0].Steps[1].ReservedBy != "":
			err = fmt.Errorf("reservations: %+v", state.Plans[0].Steps)
		case len(state.Owned) != 1 || state.Owned[0].WorkID != p.w.ID || state.Owned[0].Assignee != p.w.Assignee || state.Owned[0].Revision != p.w.Revision:
			err = fmt.Errorf("owned state: %+v", state.Owned)
		case len(state.Assigned) != 0:
			err = fmt.Errorf("root listed as assignee: %+v", state.Assigned)
		}
		p.done <- err
		return provider.Response{Content: "noted"}, nil
	}
}

type wakeWorker struct{ p *wakeState }

func (f wakeWorker) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	p := f.p
	switch p.workerCalls.Add(1) {
	case 1:
		var assigned work.Work
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Work != nil {
				assigned = *m.Envelope.Work
			}
		}
		state := wakeBlock(r)
		switch {
		case state == nil:
			return provider.Response{}, fmt.Errorf("worker woke without a state block")
		case len(state.Assigned) != 1 || state.Assigned[0].WorkID != assigned.ID || state.Assigned[0].AssignedAtRevision != assigned.AssignedAtRevision || state.Assigned[0].Revision != assigned.Revision:
			return provider.Response{}, fmt.Errorf("assigned state: %+v", state.Assigned)
		case len(state.Assigned[0].Steps) != 1 || state.Assigned[0].Steps[0].Status != work.Pending:
			return provider.Response{}, fmt.Errorf("scoped steps: %+v", state.Assigned[0].Steps)
		case len(state.Plans) != 0 || len(state.Owned) != 0:
			return provider.Response{}, fmt.Errorf("worker sees owner state: %+v", state)
		}
		return operation("send_message", map[string]any{"to": assigned.Owner, "message": "starting"})
	default:
		return provider.Response{Content: "waiting for review"}, nil
	}
}

type wakeIdle struct{}

func (wakeIdle) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: "idle"}, nil
}

func TestAgentsWakeWithTheirCurrentStateBlock(t *testing.T) {
	p := &wakeState{done: make(chan error, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.LocalTools, cfg.Web = t.TempDir(), false, nil
	s, err := harness.New(ctx, cfg, harness.Dependencies{Root: harness.AgentDependencies{Provider: wakeRoot{p}}, Implementor: harness.AgentDependencies{Provider: wakeWorker{p}}, Provider: wakeIdle{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err = s.Send(s.Root(), "Plan and assign one step"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err(), p.rootCalls.Load(), p.workerCalls.Load())
	}
}
