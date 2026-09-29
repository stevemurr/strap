package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

type experimentScript struct {
	managerCalls, workerCalls atomic.Int32
	done                      chan work.Conclusion
	w                         work.Work
	hypothesis                work.HypothesisID
}

type experimentManager struct{ p *experimentScript }

func (f experimentManager) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	switch f.p.managerCalls.Add(1) {
	case 1:
		return operation("create_agent", map[string]any{"role": "experimenter"})
	case 2:
		var created struct {
			AgentID identity.ActorID `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &created); err != nil {
			return provider.Response{}, err
		}
		return operation("assign_task", map[string]any{"kind": "experiment", "assignee": created.AgentID, "task": "Is rendering slow?", "context": nil, "expected_output": nil, "scope": nil})
	case 3:
		return operation("wait_for_input", struct{}{})
	case 4:
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Event != nil && m.Envelope.Event.Kind == work.ExperimentDelivered {
				return operation("get_conclusion", map[string]any{"conclusion_id": m.Envelope.Event.Work.LatestConclusionID})
			}
		}
		return provider.Response{}, fmt.Errorf("missing delivery notice")
	default:
		var c work.Conclusion
		if err := json.Unmarshal([]byte(lastResult(r)), &c); err != nil {
			return provider.Response{}, fmt.Errorf("conclusion read: %v %s", err, lastResult(r))
		}
		f.p.done <- c
		return provider.Response{Content: "Rendering is fine."}, nil
	}
}

type experimentWorker struct{ p *experimentScript }

func (f experimentWorker) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	p := f.p
	switch p.workerCalls.Add(1) {
	case 1:
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Work != nil {
				p.w = *m.Envelope.Work
			}
		}
		return operation("record_hypothesis", map[string]any{"work_id": p.w.ID, "statement": "Rendering is slow", "prediction": "A render takes over 100ms", "method": "Time the render script"})
	case 2:
		var receipt work.HypothesisReceipt
		if err := json.Unmarshal([]byte(lastResult(r)), &receipt); err != nil {
			return provider.Response{}, fmt.Errorf("hypothesis: %v %s", err, lastResult(r))
		}
		p.hypothesis = receipt.Hypothesis.ID
		return operation("write_file", map[string]any{"path": "bench.sh", "content": "echo rendered\n"})
	case 3:
		return operation("run_trials", map[string]any{"command": "sh bench.sh", "trials": 3, "timeout_ms": nil})
	case 4:
		var receipt struct {
			Ref string `json:"evidence_ref"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &receipt); err != nil || receipt.Ref == "" {
			return provider.Response{}, fmt.Errorf("trials: %v %s", err, lastResult(r))
		}
		return operation("record_result", map[string]any{"work_id": p.w.ID, "hypothesis_id": p.hypothesis, "verdict": "refuted", "observed": "Median render well under 100ms", "evidence_refs": []string{receipt.Ref}})
	case 5:
		var receipt work.HypothesisReceipt
		if err := json.Unmarshal([]byte(lastResult(r)), &receipt); err != nil {
			return provider.Response{}, fmt.Errorf("result: %v %s", err, lastResult(r))
		}
		return operation("submit_experiment", map[string]any{"work_id": p.w.ID, "expected_revision": receipt.WorkRevision, "summary": "Rendering is fast", "method": map[string]any{"reproduce_command": "sh bench.sh", "files": []string{"bench.sh"}}, "recommendation": nil, "proposed_steps": nil})
	default:
		return provider.Response{Content: "Delivered."}, nil
	}
}

// An experiment runs end to end in its own copy of the workspace: the
// harness file it writes travels in the conclusion and never reaches the
// workspace.
func TestExperimentMeasuresInItsCopyAndDeliversItsMethod(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := &experimentScript{done: make(chan work.Conclusion, 1)}
	cfg := testConfig(t, true)
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready"), Manager: harness.AgentDependencies{Provider: experimentManager{p}}, Experimenter: harness.AgentDependencies{Provider: experimentWorker{p}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	startManager(t, s, "Is rendering slow?")
	c := await(t, p.done, "experiment conclusion")
	if len(c.Hypotheses) != 1 || c.Hypotheses[0].Result.Verdict != work.Refuted || len(c.Method.Files) != 1 || c.Method.Files[0].Content != "echo rendered\n" {
		t.Fatalf("%+v", c)
	}
	if _, err = os.Stat(filepath.Join(cfg.Dir, "bench.sh")); !os.IsNotExist(err) {
		t.Fatal("the experimenter's harness reached the workspace:", err)
	}
	if got, err := s.GetWork(ctx, s.Manager(), c.WorkID); err != nil || got.State != work.Delivered {
		t.Fatal(got, err)
	}
}

// Auditors and experimenters write only in their copies; only the
// experimenter times runs and records hypotheses.
func TestIsolatedRoleTools(t *testing.T) {
	s, err := harness.New(context.Background(), testConfig(t, true), harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	c := s.Configuration()
	if c.Experimenter == nil {
		t.Fatal("experimenter missing with local tools")
	}
	names := func(r harness.RoleConfiguration) []string {
		var out []string
		for _, d := range r.Tools {
			out = append(out, d.Name)
		}
		return out
	}
	auditor, experimenter := names(c.Auditor), names(*c.Experimenter)
	for _, want := range []string{"shell", "write_file", "edit_file", "read_file", "submit_audit"} {
		if !slices.Contains(auditor, want) {
			t.Errorf("auditor lacks %s", want)
		}
	}
	for _, want := range []string{"shell", "write_file", "run_trials", "record_hypothesis", "record_result", "submit_experiment", "get_conclusion"} {
		if !slices.Contains(experimenter, want) {
			t.Errorf("experimenter lacks %s", want)
		}
	}
	for _, unwanted := range []string{"run_trials", "record_hypothesis"} {
		if slices.Contains(auditor, unwanted) {
			t.Errorf("auditor holds %s", unwanted)
		}
	}
	for _, unwanted := range []string{"submit_work", "submit_audit", "submit_brief", "web_search", "deep_research"} {
		if slices.Contains(experimenter, unwanted) {
			t.Errorf("experimenter holds %s", unwanted)
		}
	}
	// The reviewer reads the workspace for the manager, which cannot; the web
	// researcher reaches the web and nothing else.
	if c.Reviewer == nil {
		t.Fatal("reviewer missing with local tools")
	}
	reviewer := names(*c.Reviewer)
	for _, want := range []string{"read_file", "glob", "grep_search", "list_directory", "submit_brief"} {
		if !slices.Contains(reviewer, want) {
			t.Errorf("reviewer lacks %s", want)
		}
	}
	for role, tools := range map[string][]string{"reviewer": reviewer, "manager": names(c.Manager), "web_researcher": names(c.WebResearcher)} {
		for _, name := range tools {
			reads := name == "read_file" || name == "glob" || name == "grep_search" || name == "list_directory" || strings.HasPrefix(name, "lsp_")
			if name == "shell" || name == "write_file" || name == "edit_file" || name == "web_search" || role != "reviewer" && reads {
				t.Errorf("%s holds %s", role, name)
			}
		}
	}
	// Without local tools there is no copy to measure in.
	offline, err := harness.New(context.Background(), testConfig(t, false), harness.Dependencies{Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer offline.Close(context.Background())
	if _, err = offline.CreateAgent(context.Background(), offline.Manager(), roster.CreateRequest{Role: roster.Experimenter}); err == nil {
		t.Fatal("an experimenter was created without local tools")
	}
}
