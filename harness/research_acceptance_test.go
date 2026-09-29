package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type researchAcceptance struct {
	rootCalls, workerCalls atomic.Int32
	done                   chan string
	w                      work.Work
	inline                 bool // deliver the finding with submit_brief instead of reporting it first
}

func operation(name string, args any) (provider.Response, error) {
	raw, err := tool.MarshalInput(args)
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: name, Name: name, Arguments: raw}}}, err
}
func lastResult(r provider.Request) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "tool" {
			return r.Messages[i].Content.Text()
		}
	}
	return ""
}

type acceptanceRoot struct{ p *researchAcceptance }

func (f acceptanceRoot) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	switch f.p.rootCalls.Add(1) {
	case 1:
		return operation("create_agent", map[string]any{"role": "web_researcher"})
	case 2:
		var created struct {
			AgentID identity.ActorID `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &created); err != nil {
			return provider.Response{}, err
		}
		return operation("assign_task", map[string]any{"kind": "web_research", "assignee": created.AgentID, "task": "Diagnose the fixture and deliver findings", "context": nil, "expected_output": nil, "scope": nil})
	case 3:
		return operation("wait_for_input", struct{}{})
	case 4:
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Progress != nil && len(m.Envelope.Progress.Briefs) > 0 {
				return operation("get_brief", map[string]any{"brief_id": m.Envelope.Progress.Briefs[0].BriefID, "max_bytes": nil})
			}
		}
		return provider.Response{}, fmt.Errorf("missing delivery notice")
	default:
		// The brief read returns the brief followed by the finding it cites.
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		var brief work.Brief
		var finding work.ProgressFinding
		if err := json.Unmarshal([]byte(lastResult(r)), &page); err != nil || len(page.Items) != 2 {
			return provider.Response{}, fmt.Errorf("brief read: %v %s", err, lastResult(r))
		}
		if json.Unmarshal(page.Items[0], &brief) != nil || json.Unmarshal(page.Items[1], &finding) != nil || len(brief.FindingIDs) != 1 || finding.ID != brief.FindingIDs[0] || !strings.Contains(finding.Claim, "fixture") {
			return provider.Response{}, fmt.Errorf("brief read: %s", lastResult(r))
		}
		f.p.done <- lastResult(r)
		return provider.Response{Content: "The delivered finding answers the question; research is delivered."}, nil
	}
}

type acceptanceWorker struct{ p *researchAcceptance }

func (f acceptanceWorker) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	p := f.p
	switch p.workerCalls.Add(1) {
	case 1:
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Work != nil {
				p.w = *m.Envelope.Work
			}
		}
		limitation := "Read from the task, not from a source"
		if p.inline {
			return operation("submit_brief", tool.SubmitBriefInput{WorkTarget: work.WorkTarget{ID: p.w.ID, ExpectedRevision: p.w.Revision}, Summary: "Diagnostic finding delivered", Findings: []tool.ProgressFindingDraftInput{{Claim: "The fixture needs no external dependency", Basis: work.Inferred, Limitation: &limitation}}})
		}
		return operation("report_work_progress", tool.ReportWorkProgressInput{WorkID: p.w.ID, Findings: []tool.ProgressFindingDraftInput{{Claim: "The fixture needs no external dependency", Basis: work.Inferred, Limitation: &limitation}}})
	case 2:
		var receipt work.ReportWorkProgressResult
		if err := json.Unmarshal([]byte(lastResult(r)), &receipt); err != nil || len(receipt.FindingIDs) != 1 {
			return provider.Response{}, fmt.Errorf("report: %v %s", err, lastResult(r))
		}
		return operation("submit_brief", tool.SubmitBriefInput{WorkTarget: work.WorkTarget{ID: p.w.ID, ExpectedRevision: receipt.WorkRevision}, Summary: "Diagnostic finding delivered", FindingIDs: receipt.FindingIDs})
	default:
		return operation("wait_for_input", struct{}{})
	}
}
func TestResearchEndToEndToolsAndCoveredTimer(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "reported findings", true: "inline findings"}[inline], func(t *testing.T) { researchEndToEnd(t, inline) })
	}
}

func researchEndToEnd(t *testing.T, inline bool) {
	p := &researchAcceptance{done: make(chan string, 4), inline: inline}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := testConfig(t, true)
	cfg.WorkProgressReporting.BatchWindow = 2 * time.Second
	s, err := harness.New(ctx, cfg, harness.Dependencies{Manager: harness.AgentDependencies{Provider: acceptanceRoot{p}}, WebResearcher: harness.AgentDependencies{Provider: acceptanceWorker{p}}, Provider: textResponse("ready")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	manager := startManager(t, s, "Investigate the fixture with a researcher")
	await(t, p.done, "research delivery")
	// The old finding timer must not create an exchange after delivery and answer.
	select {
	case extra := <-p.done:
		t.Fatal("obsolete timer woke root", extra)
	case <-time.After(2100 * time.Millisecond):
	}
	page, err := s.ListWork(ctx, manager, work.ListQuery{Kind: work.WebResearch})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != work.Delivered {
		t.Fatal(page, err)
	}
	// The researcher's turn ends at its successful submit_brief.
	calls := int32(2)
	if inline {
		calls = 1
	}
	if p.rootCalls.Load() != 5 || p.workerCalls.Load() != calls {
		t.Fatal(p.rootCalls.Load(), p.workerCalls.Load())
	}
}
