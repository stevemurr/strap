package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type evidenceScript struct {
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
		args, _ := json.Marshal(tool.ResearchDiagnosticArgs{WorkID: assigned.ID, AssignedAtRevision: assigned.AssignedAtRevision, Command: "printf '%05000ddecisive-error\\n' 0"})
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "diagnostic", Name: "shell", Arguments: args}}}, nil
	}
	p.receipt <- r.Messages[len(r.Messages)-1].Content.Text()
	return provider.Response{ToolCalls: []provider.ToolCall{{ID: "wait", Name: "wait_for_input", Arguments: json.RawMessage(`{}`)}}}, nil
}
func TestExecutionEvidencePagesAndPassiveArchive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &evidenceScript{receipt: make(chan string, 2)}
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: idle{}, Researcher: harness.AgentDependencies{Provider: p}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	worker := createWorker(t, s, roster.Researcher)
	w, err := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Research, Assignee: worker, Task: "Run diagnostic"})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	select {
	case raw = <-p.receipt:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var receipt struct {
		EvidenceRef string `json:"evidence_ref"`
	}
	if err = json.Unmarshal([]byte(raw), &receipt); err != nil || receipt.EvidenceRef == "" {
		t.Fatal(raw, err)
	}
	r, err := s.ReportWorkProgress(ctx, worker, work.ReportWorkProgressRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, AssignedAtRevision: w.AssignedAtRevision, Findings: []work.ProgressFindingDraft{{Claim: "Diagnostic contains decisive error", Basis: work.Observed, Evidence: []work.EvidenceRef{{URI: receipt.EvidenceRef}}}}})
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
