package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
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
	finding                work.ProgressFindingID
	ref                    string
}

func operation(name string, args any) (provider.Response, error) {
	raw, err := json.Marshal(args)
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
		return operation("create_agent", map[string]any{"role": "researcher"})
	case 2:
		var created struct {
			AgentID identity.ActorID `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &created); err != nil {
			return provider.Response{}, err
		}
		return operation("assign_work", map[string]any{"kind": "research", "assignee": created.AgentID, "task": "Diagnose the fixture and deliver findings"})
	case 3:
		return operation("wait_for_input", struct{}{})
	case 4:
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Progress != nil && len(m.Envelope.Progress.Briefs) > 0 {
				return operation("get_research_brief", map[string]any{"brief_id": m.Envelope.Progress.Briefs[0].BriefID})
			}
		}
		return provider.Response{}, fmt.Errorf("missing delivery notice")
	case 5:
		var page struct {
			Items []work.ResearchBrief `json:"items"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &page); err != nil || len(page.Items) != 1 {
			return provider.Response{}, fmt.Errorf("brief read: %v %s", err, lastResult(r))
		}
		return operation("get_work_progress", map[string]any{"mode": "finding", "finding_id": page.Items[0].FindingIDs[0]})
	case 6:
		var page struct {
			Items []work.ProgressFinding `json:"items"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &page); err != nil || len(page.Items) != 1 {
			return provider.Response{}, fmt.Errorf("finding read: %v", err)
		}
		return operation("get_work_progress", map[string]any{"mode": "evidence", "evidence_ref": page.Items[0].Evidence[0].URI})
	default:
		if !strings.Contains(lastResult(r), "diagnostic-result") {
			return provider.Response{}, fmt.Errorf("missing evidence")
		}
		f.p.done <- lastResult(r)
		return provider.Response{Content: "The recorded diagnostic supports the research finding; research is delivered."}, nil
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
		return operation("shell", map[string]any{"work_id": p.w.ID, "assigned_at_revision": p.w.AssignedAtRevision, "command": "printf diagnostic-result"})
	case 2:
		var receipt struct {
			Ref string `json:"evidence_ref"`
		}
		if err := json.Unmarshal([]byte(lastResult(r)), &receipt); err != nil {
			return provider.Response{}, err
		}
		p.ref = receipt.Ref
		return operation("report_work_progress", work.ReportWorkProgressRequest{WorkTarget: work.WorkTarget{ID: p.w.ID, ExpectedRevision: p.w.Revision}, AssignedAtRevision: p.w.AssignedAtRevision, Findings: []work.ProgressFindingDraft{{Claim: "Diagnostic returned the fixture result", Basis: work.Observed, Evidence: []work.EvidenceRef{{URI: p.ref}}}}})
	case 3:
		var receipt work.ReportWorkProgressResult
		if err := json.Unmarshal([]byte(lastResult(r)), &receipt); err != nil || len(receipt.FindingIDs) != 1 {
			return provider.Response{}, fmt.Errorf("report: %v %s", err, lastResult(r))
		}
		return operation("submit_research", work.SubmitResearchRequest{WorkTarget: work.WorkTarget{ID: p.w.ID, ExpectedRevision: receipt.WorkRevision}, AssignedAtRevision: p.w.AssignedAtRevision, Summary: "Diagnostic finding delivered", FindingIDs: receipt.FindingIDs})
	default:
		return operation("wait_for_input", struct{}{})
	}
}
func TestResearchEndToEndToolsAndCoveredTimer(t *testing.T) {
	p := &researchAcceptance{done: make(chan string, 4)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	cfg.WorkProgressReporting.BatchWindow = 2 * time.Second
	s, err := harness.New(ctx, cfg, harness.Dependencies{Root: harness.AgentDependencies{Provider: acceptanceRoot{p}}, Researcher: harness.AgentDependencies{Provider: acceptanceWorker{p}}, Provider: idle{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err = s.Send(s.Root(), "Investigate the fixture with a researcher"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The old finding timer must not create an exchange after delivery and answer.
	select {
	case extra := <-p.done:
		t.Fatal("obsolete timer woke root", extra)
	case <-time.After(2100 * time.Millisecond):
	}
	page, err := s.ListWork(ctx, s.Root(), work.ListQuery{Kind: work.Research})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != work.Delivered {
		t.Fatal(page, err)
	}
	if p.rootCalls.Load() != 7 || p.workerCalls.Load() != 4 {
		t.Fatal(p.rootCalls.Load(), p.workerCalls.Load())
	}
}
