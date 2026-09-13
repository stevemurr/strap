package inspection_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"

	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/provider"
)

// sealedScript makes one failing tool call and then answers with enough text to
// be chunked out of its record.
type sealedScript struct{ step atomic.Int32 }

func (p *sealedScript) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	if p.step.Add(1) == 1 {
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "inspect_agent", Arguments: json.RawMessage(`{"agent_id":"missing"}`)}}}, nil
	}
	return provider.Response{Content: strings.Repeat("long answer ", 2000)}, nil
}

// sealedTrace runs a session to completion, seals it, and serves the archive.
func sealedTrace(t *testing.T) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()
	cfg := harness.DefaultConfig()
	cfg.Dir, cfg.Web, cfg.LocalTools = t.TempDir(), nil, false
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	// The smallest permitted queue forces large payloads out into content chunks.
	cfg.Events.Queue = eventlog.Limits{Entries: 64, Bytes: 4096}
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: &sealedScript{}})
	if err != nil {
		t.Fatal(err)
	}
	root := string(s.Root())
	if _, err := s.Send(s.Root(), "answer at length"); err != nil {
		t.Fatal(err)
	}
	// Let the turn finish so the trace holds its tool call and full answer.
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(wait)
		if err != nil {
			t.Fatal("session never settled", err)
		}
		if c, ok := e.(conversation.AgentStateChanged); ok && c.Agent == s.Root() && c.State == agent.Idle {
			break
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := s.Trace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close(ctx)
		_ = s.Dispose(ctx)
	})
	return inspection.Handler(reader), root
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

// A sealed session reports its outcome, and its recorded tool failures and
// chunked output text are all readable from the archive.
func TestSealedTraceReportsOutcomeFailuresAndChunkedText(t *testing.T) {
	h, root := sealedTrace(t)

	session := decode[inspection.SessionView](t, get(t, h, "/"))
	if session.Outcome == nil {
		t.Fatal("a sealed session reported no outcome", session)
	}
	if len(session.Configuration) == 0 {
		t.Fatal("the archive did not retain the session configuration")
	}

	tools := decode[inspection.ToolPage](t, get(t, h, "/tools"))
	if len(tools.Items) == 0 {
		t.Fatal("the archive recorded no tool activity")
	}
	failed := false
	for _, item := range tools.Items {
		if item.Error != nil {
			failed = true
			if item.Error.Code != "tool_error" {
				t.Fatal("a tool failure was recorded under the wrong code", item.Error)
			}
		}
		// Each listed invocation is individually readable.
		one := decode[inspection.ToolView](t, get(t, h, "/tools/"+string(item.InvocationID)))
		if one.InvocationID != item.InvocationID || one.Name != item.Name {
			t.Fatal("listed and inspected tool views disagree", one, item)
		}
		if one.StartedAt == nil {
			t.Fatal("an inspected tool had no start time", one)
		}
	}
	if !failed {
		t.Fatal("the failing tool call was not recorded as an error")
	}

	outputs := decode[inspection.OutputPage](t, get(t, h, "/outputs"))
	if len(outputs.Items) == 0 {
		t.Fatal("the archive recorded no output")
	}
	var long inspection.OutputView
	for _, o := range outputs.Items {
		if o.TextBytes > long.TextBytes {
			long = o
		}
	}
	if long.TextBytes < 4096 {
		t.Fatal("no output was large enough to be chunked", long.TextBytes)
	}
	base := fmt.Sprintf("/outputs/%s/%d/text", long.ID.Agent, long.ID.Call)

	// Reading the text back in bounded pages reassembles exactly what was written.
	var assembled strings.Builder
	offset := uint64(0)
	for i := 0; i < 100; i++ {
		page := decode[inspection.TextPage](t, get(t, h, fmt.Sprintf("%s?offset=%d&max_bytes=1024", base, offset)))
		assembled.WriteString(page.Text)
		if page.End {
			break
		}
		if page.Next <= offset && page.Text == "" {
			t.Fatal("paging made no progress at offset", offset)
		}
		offset = page.Next
	}
	if uint64(assembled.Len()) != long.TextBytes {
		t.Fatal("reassembled text does not match the recorded byte count", assembled.Len(), long.TextBytes)
	}
	if !strings.Contains(assembled.String(), "long answer") {
		t.Fatal("reassembled text is not the recorded answer")
	}

	// An unknown channel and an out-of-range offset are refused.
	if w := get(t, h, base+"?channel=telepathy"); w.Code != 400 && w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := get(t, h, fmt.Sprintf("%s?offset=%d", base, long.TextBytes+1<<20)); w.Code < 400 {
		t.Fatal("read past the end of the recorded text", w.Code, w.Body.String())
	}
	// The root agent's own inspection still resolves from the archive.
	if w := get(t, h, "/agents/"+root); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
