package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/provider"
)

type editScript struct {
	step       atomic.Int32
	dir        string
	modelError chan string
}

func (p *editScript) Submit(ctx context.Context, r provider.Request, observer provider.Observer) (provider.Response, error) {
	switch p.step.Add(1) {
	case 1:
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "reused", Name: "write_file", Arguments: json.RawMessage(`{"input":{"path":"file","content":"original"}}`)}}}, nil
	case 2:
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "reused", Name: "edit_file", Arguments: json.RawMessage(`{"input":{"path":"file","old":"missing","new":"replacement"}}`)}}}, nil
	default:
		raw, _ := json.Marshal(r.Messages[len(r.Messages)-1])
		p.modelError <- string(raw)
		err := os.WriteFile(filepath.Join(p.dir, "file"), []byte("later contents"), 0600)
		return provider.Response{Content: "done"}, err
	}
}
func TestDurableToolDiagnosticsCorrelateRepeatedProviderIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")
	p := &editScript{dir: dir, modelError: make(chan string, 1)}
	cfg := harness.DefaultConfig()
	cfg.Dir = dir
	cfg.Web = nil
	cfg.Events.JSONLPath = path
	s, err := harness.New(context.Background(), cfg, harness.Dependencies{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	if _, err = s.Send(s.Root(), "run"); err != nil {
		t.Fatal(err)
	}
	if result := await(t, p.modelError, "script did not reach error response"); !strings.Contains(result, "old text was not found") || strings.Contains(result, "sha256") || strings.Contains(result, "original") {
		t.Fatal(result)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader, err := eventlog.OpenJSONL(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	page, err := reader.Read(context.Background(), eventlog.Query{Limit: 1000})
	if err != nil || !page.Sealed {
		t.Fatal(page, err)
	}
	starts, finishes := map[string]bool{}, map[string]bool{}
	success, failure := false, false
	for _, e := range page.Events {
		if e.Kind != "tool" {
			continue
		}
		v, err := eventcodec.DecodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		a := v.(conversation.ToolEvent).Activity
		if a.InvocationID == "" {
			t.Fatal("missing invocation identity")
		}
		if a.FinishedAt.IsZero() {
			starts[a.InvocationID] = true
			continue
		}
		finishes[a.InvocationID] = true
		if a.Call.Name == "write_file" && a.Err == nil {
			success = true
		}
		if a.Call.Name == "edit_file" && a.Err != nil {
			failure = true
			if a.Err.Error() != "old text was not found" || a.Diagnostic == nil || a.Diagnostic.Edit.Before != "original" {
				t.Fatal(a)
			}
		}
	}
	if !success || !failure || len(starts) != 2 || len(finishes) != 2 {
		t.Fatal(success, failure, starts, finishes)
	}
	for id := range starts {
		if !finishes[id] {
			t.Fatal("missing finish", id)
		}
	}
}
