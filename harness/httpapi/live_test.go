package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/httpapi"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
)

// Opt-in: all commands and observations use real HTTP, and the service uses its
// real provider factory. Ordinary tests skip this external model dependency.
// STRAP_LIVE_BASE_URL=http://192.168.1.237:8355 go test -race ./harness/httpapi -run TestLiveModelHTTP -count=1 -v
func TestLiveModelHTTP(t *testing.T) {
	base := os.Getenv("STRAP_LIVE_BASE_URL")
	if base == "" {
		t.Skip("set STRAP_LIVE_BASE_URL to run the live model HTTP smoke test")
	}
	cfg := harness.DefaultConfig()
	cfg.Model.BaseURL = base
	if model := os.Getenv("STRAP_LIVE_MODEL"); model != "" {
		cfg.Model.Model = model
	}
	cfg.Model.Timeout = 45 * time.Second
	cfg.Model.Generation.MaxTokens = ptr(512)
	cfg.Model.Generation.EnableThinking = ptr(false)
	cfg.Model.Generation.Temperature = ptr(0.0)
	cfg.Web = nil
	cfg.Dir = t.TempDir()
	cfg.Events.JSONLPath = filepath.Join(cfg.Dir, "trace.jsonl")
	cfg.Root.Prompt = prompt.Prompt{Role: "Follow the user's smoke-test instructions precisely. Keep responses short. You have a callable read_file tool. Use it when the current user message requests a file read. Each user message is a separate step: a no-tool instruction in an earlier step does not prohibit tool use in a later step. Do not delegate work, change files, or run shell commands."}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(entropy[:])
	fileValue := "LIVE_FILE_VALUE_9D72"
	if err := os.WriteFile(filepath.Join(cfg.Dir, "smoke.txt"), []byte(fileValue+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	service, err := httpapi.New(ctx, httpapi.Options{DefaultConfig: cfg, Authorize: httpapi.BearerToken(token)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := service.Close(cleanup); err != nil {
			t.Error(err)
		}
		server.Close()
	})
	client := &http.Client{}
	call := func(method, path string, body any, status int, out any) {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(raw)
		}
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, input)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, response.StatusCode, raw)
		}
		if out != nil {
			if err = json.Unmarshal(raw, out); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
		}
	}
	var view httpapi.SessionView
	call("POST", "/sessions", httpapi.CreateRequest{}, 201, &view)
	if view.State != harness.Open || view.Root == "" || view.Config.Root.InjectedProvider {
		t.Fatalf("not a live session: %+v", view)
	}
	path := "/sessions/" + view.ID
	t.Logf("live HTTP session=%s model=%s backend=%s", view.ID, view.Config.Root.Model.Model, view.Config.Root.Model.Backend)
	var cursor uint64
	var stream *http.Response
	var decoder *json.Decoder
	attach := func() {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s%s/events/stream?after=%d", server.URL, path, cursor), nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		stream, err = client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if stream.StatusCode != 200 {
			stream.Body.Close()
			t.Fatalf("stream status %s", stream.Status)
		}
		decoder = json.NewDecoder(stream.Body)
	}
	defer func() {
		if stream != nil {
			stream.Body.Close()
		}
	}()
	attach()
	starts := map[string]bool{}
	toolFinishes, usageCalls, tokenCounts := 0, 0, 0
	sawFile, terminal := false, false
	next := func() (conversation.Event, bool) {
		t.Helper()
		var record httpapi.StreamRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("stream after %d: %v", cursor, err)
		}
		if record.Type == "error" {
			t.Fatalf("stream error: %+v", record.Error)
		}
		if record.Type == "end" {
			return nil, true
		}
		if record.Type != "event" || record.Event == nil || record.Event.Sequence != cursor+1 {
			t.Fatalf("event gap after %d: %+v", cursor, record)
		}
		e := *record.Event
		cursor = e.Sequence
		if e.Kind == "session_closed" {
			terminal = true
		}
		v, err := eventcodec.DecodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		switch v := v.(type) {
		case conversation.AgentExited:
			if !terminal && v.Err != nil && !strings.Contains(v.Err.Error(), "context canceled") {
				t.Fatalf("model agent exited: %v", v.Err)
			}
		case conversation.ToolEvent:
			a := v.Activity
			if a.InvocationID == "" || e.Correlation != a.InvocationID {
				t.Fatal("missing tool invocation correlation")
			}
			if a.FinishedAt.IsZero() {
				starts[a.InvocationID] = true
				break
			}
			if !starts[a.InvocationID] {
				t.Fatal("tool finish without start")
			}
			toolFinishes++
			if a.Err != nil {
				t.Fatalf("live tool failure: %v", a.Err)
			}
			if a.Call.Name != "read_file" {
				t.Fatalf("unexpected live tool: %s", a.Call.Name)
			}
			raw, _ := json.Marshal(a.Result)
			if strings.Contains(string(raw), fileValue) {
				sawFile = true
			}
			t.Logf("tool=%s invocation=%s completed", a.Call.Name, a.InvocationID)
		case conversation.UsageEvent:
			u := v.Observation.Usage
			if u == nil || u.InputTokens == nil || u.OutputTokens == nil || *u.InputTokens <= 0 || *u.OutputTokens <= 0 {
				t.Fatalf("missing live usage: %+v", u)
			}
			usageCalls++
			t.Logf("model call=%d input=%d output=%d", v.Observation.Call, *u.InputTokens, *u.OutputTokens)
		case conversation.ContextTokensEvent:
			if v.Error != "" || v.Count <= 0 {
				t.Fatalf("automatic telemetry failed: %+v", v)
			}
			tokenCounts++
			t.Logf("context revision=%d tokens=%d", v.Revision, v.Count)
		}
		return v, false
	}
	send := func(text string) message.Receipt {
		var receipt message.Receipt
		call("POST", path+"/messages", httpapi.SendRequest{To: view.Root, Content: text}, 200, &receipt)
		if receipt.MessageID == "" {
			t.Fatal("missing delivery receipt")
		}
		return receipt
	}
	waitReply := func(want string) {
		t.Helper()
		for {
			e, end := next()
			if end {
				t.Fatal("session ended before reply")
			}
			if reply, ok := e.(conversation.MessageEvent); ok && reply.Message.From == view.Root && reply.Message.To == message.User && reply.Message.Kind == message.Reply {
				if strings.TrimSpace(reply.Message.Content) != want {
					t.Fatalf("reply=%q, want %q", reply.Message.Content, want)
				}
				t.Logf("reply=%q", reply.Message.Content)
				return
			}
		}
	}
	send("For this first step only, reply with HTTP_SMOKE_OK. No tools are needed for this step.")
	waitReply("HTTP_SMOKE_OK")
	if toolFinishes != 0 {
		t.Fatal("plain reply unexpectedly used tools")
	}
	stream.Body.Close()
	call("GET", path, nil, 200, &view)
	if view.State != harness.Open {
		t.Fatal("disconnect stopped the session")
	}
	t.Logf("disconnected observer at sequence=%d; session remains open", cursor)
	attach()
	receipt := send("The first step is complete. For this separate step, call read_file once to read smoke.txt. Then reply with only the exact value stored in that file, without line numbers or extra text. Do not use any other tools.")
	waitReply(fileValue)
	for tokenCounts == 0 {
		_, end := next()
		if end {
			t.Fatal("missing automatic context count")
		}
	}
	if !sawFile || toolFinishes != 1 || usageCalls < 3 {
		t.Fatalf("incomplete live evidence: file=%v tools=%d model_calls=%d", sawFile, toolFinishes, usageCalls)
	}
	var consumed message.Receipt
	call("GET", path+"/receipts/"+string(receipt.MessageID), nil, 200, &consumed)
	if consumed.Status != message.Consumed {
		t.Fatalf("receipt not consumed: %+v", consumed)
	}
	call("POST", path+"/close", nil, 200, nil)
	for {
		_, end := next()
		if end {
			break
		}
	}
	if !terminal {
		t.Fatal("stream ended without terminal record")
	}
	call("GET", path, nil, 200, &view)
	if view.State != harness.Closed || !view.Capture.Sealed || view.Capture.CaptureError != "" || view.Capture.Omitted != 0 || view.Outcome == nil || view.Outcome.Error != "" || view.Outcome.CleanupError != "" {
		t.Fatalf("unclean finalization: %+v", view)
	}
	var page eventlog.Page
	call("GET", path+"/events?limit=1000", nil, 200, &page)
	if !page.Sealed || page.Latest != cursor || uint64(len(page.Events)) != cursor {
		t.Fatalf("stream/page mismatch: latest=%d cursor=%d count=%d", page.Latest, cursor, len(page.Events))
	}
	call("POST", path+"/dispose", nil, 200, nil)
	call("GET", path, nil, 200, &view)
	if view.State != harness.Disposed || !view.Capture.Disposed || view.Outcome == nil {
		t.Fatalf("incomplete disposal: %+v", view)
	}
	call("GET", path+"/events?limit=1", nil, 410, nil)
	t.Logf("PASS: %d contiguous events; %d model calls; %d tool call; %d context count; sealed and disposed", cursor, usageCalls, toolFinishes, tokenCounts)
}
