package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type evidenceScript struct {
	command string
	calls   atomic.Int32
	receipt chan string
}

func (p *evidenceScript) Submit(_ context.Context, r provider.Request, _ provider.Observer) (provider.Response, error) {
	if p.calls.Add(1) == 1 {
		var assigned *work.Work
		for _, m := range r.Messages {
			if m.Envelope != nil && m.Envelope.Work != nil {
				assigned = m.Envelope.Work
			}
		}
		command := p.command
		if command == "" {
			command = "printf '%05000ddecisive-error\\n' 0"
		}
		args, _ := tool.MarshalInput(tool.ResearchDiagnosticArgs{WorkID: assigned.ID, Command: command})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "diagnostic", Name: "shell", Arguments: args}}}, nil
	}
	p.receipt <- r.Messages[len(r.Messages)-1].Content.Text()
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: "wait", Name: "wait_for_input", Arguments: json.RawMessage(`{"input":{}}`)}}}, nil
}
func TestExecutionEvidencePagesAndPassiveArchive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &evidenceScript{receipt: make(chan string, 2)}
	cfg := testConfig(t, true)
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready"), Researcher: harness.AgentDependencies{Provider: p}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	worker := createWorker(t, s, roster.Researcher)
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: worker, Task: "Run diagnostic"})
	if err != nil {
		t.Fatal(err)
	}
	raw := await(t, p.receipt, "evidence receipt")
	var receipt struct {
		EvidenceRef string `json:"evidence_ref"`
	}
	if err = json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.EvidenceRef == "" {
		t.Fatal(raw, err)
	}
	r, err := s.ReportWorkProgress(ctx, worker, work.ReportWorkProgressRequest{WorkID: w.ID, Findings: []work.ProgressFindingDraft{{Claim: "Diagnostic contains decisive error", Basis: work.Observed, Evidence: []work.EvidenceRef{{URI: receipt.EvidenceRef}}}}})
	if err != nil {
		t.Fatal(err)
	}
	q := inspection.ProgressQuery{Mode: "evidence", EvidenceRef: receipt.EvidenceRef, MaxBytes: 2048}
	var body strings.Builder
	for {
		page, err := s.ReadWorkProgress(ctx, s.Root(), q)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) > 2048 {
			t.Fatal("page budget")
		}
		if page.Fragment != nil {
			body.WriteString(page.Fragment.Text)
		}
		for _, item := range page.Items {
			body.Write(item)
		}
		if page.NextCursor == "" {
			break
		}
		q = inspection.ProgressQuery{Mode: "continue", Cursor: page.NextCursor}
	}
	var decoded struct {
		Result tool.Result `json:"result"`
	}
	if err = json.Unmarshal([]byte(body.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	var capture tool.ShellResult
	if err = json.Unmarshal([]byte(decoded.Result.Captured.Text()), &capture); err != nil {
		t.Fatal(err)
	}
	if strings.Index(capture.Output, "decisive-error") < 4096 || !strings.Contains(body.String(), receipt.EvidenceRef) {
		t.Fatal("lost retained evidence")
	}
	first, err := s.ReadWorkProgress(ctx, worker, inspection.ProgressQuery{Mode: "evidence", EvidenceRef: receipt.EvidenceRef, MaxBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatal("live reader invoked model", p.calls.Load())
	}
	replacement := createWorker(t, s, roster.Researcher)
	_, err = s.ReassignWork(ctx, s.Root(), work.ReassignRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: r.WorkRevision}, Assignee: replacement})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadWorkProgress(ctx, worker, inspection.ProgressQuery{Mode: "continue", Cursor: first.NextCursor}); !errors.Is(err, work.ErrForbidden) {
		t.Fatal("cursor retained access", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	archiveCalls := p.calls.Load()
	archive, err := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(context.Background())
	reader, err := inspection.NewProgressReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Read(ctx, s.Root(), inspection.ProgressQuery{Mode: "evidence", EvidenceRef: receipt.EvidenceRef})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if p.calls.Load() != archiveCalls {
		t.Fatal("reader invoked model", p.calls.Load())
	}
}

func TestStoppedResearcherRetainsExecutionEvidenceWithoutReport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := testConfig(t, true)
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	p := &evidenceScript{command: "printf partial-evidence; printf ready >ready; sleep 30", receipt: make(chan string, 2)}
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: textResponse("ready"), Researcher: harness.AgentDependencies{Provider: p}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	worker := createWorker(t, s, roster.Researcher)
	_, err = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: worker, Task: "Inspect"})
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err = os.Stat(filepath.Join(cfg.Dir, "ready")); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if _, err = s.StopAgent(worker); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	archive, err := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(context.Background())
	v, err := archive.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := v.ListTools(ctx, inspection.ToolQuery{Agent: worker, Name: "shell"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Execution == nil {
		t.Fatal(page, err)
	}
	evidence, err := v.GetExecutionEvidence(ctx, s.Root(), page.Items[0].Execution.EvidenceRef)
	if err != nil {
		t.Fatal(err)
	}
	var captured tool.ShellResult
	if err = json.Unmarshal([]byte(evidence.Execution.Activity.Result.Captured.Text()), &captured); err != nil {
		t.Fatal(err)
	}
	if evidence.Execution.Activity.Err == nil || !captured.Cancelled || captured.Output != "partial-evidence" || p.calls.Load() != 1 {
		t.Fatal(captured, evidence.Execution.Activity.Err, p.calls.Load())
	}
}
