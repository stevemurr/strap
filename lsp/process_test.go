package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

type stdioFixture struct{}

func (stdioFixture) Read(b []byte) (int, error)  { return os.Stdin.Read(b) }
func (stdioFixture) Write(b []byte) (int, error) { return os.Stdout.Write(b) }
func (stdioFixture) Close() error                { return nil }

// The test executable doubles as a real stdio peer. No installed server is needed
// to exercise callbacks, cancellation, framing, synchronization, and process exit.
func TestLanguageServerProcess(t *testing.T) {
	if os.Getenv("STRAP_LSP_FIXTURE") != "1" {
		return
	}
	logPath := os.Getenv("STRAP_LSP_TRACE")
	trace := func(method string, params any) {
		raw, _ := json.Marshal(map[string]any{"method": method, "params": params})
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = f.Write(append(raw, '\n'))
			_ = f.Close()
		}
	}
	mode := os.Getenv("STRAP_LSP_MODE")
	outlineRequests := 0
	docs := map[string]struct {
		Text    string
		Version int
	}{}
	cancels := map[jsonrpc2.ID]chan struct{}{}
	var mu sync.Mutex
	handler := fixtureHandler(func(ctx context.Context, c *jsonrpc2.Conn, r *jsonrpc2.Request) (any, error) {
		var p map[string]json.RawMessage
		if r.Params != nil {
			_ = json.Unmarshal(*r.Params, &p)
		}
		trace(r.Method, p)
		switch r.Method {
		case "initialize":
			if mode == "malformed" {
				_, _ = fmt.Fprint(os.Stdout, "Content-Length: 999999999\r\n\r\n")
				return nil, nil
			}
			return map[string]any{"capabilities": map[string]any{"positionEncoding": "utf-16", "textDocumentSync": 2, "hoverProvider": true, "documentSymbolProvider": true, "definitionProvider": true, "referencesProvider": true}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}, nil
		case "textDocument/didOpen", "textDocument/didChange":
			var d struct {
				URI, Text string
				Version   int
			}
			_ = json.Unmarshal(p["textDocument"], &d)
			if r.Method == "textDocument/didChange" {
				var changes []struct {
					Text  string
					Range wireRange
				}
				_ = json.Unmarshal(p["contentChanges"], &changes)
				if len(changes) > 0 {
					d.Text = changes[0].Text
				}
			}
			docs[d.URI] = struct {
				Text    string
				Version int
			}{d.Text, d.Version}
			params := map[string]any{"uri": d.URI, "diagnostics": []any{}}
			if mode != "versionless" {
				params["version"] = d.Version
			}
			if strings.Contains(d.Text, "BROKEN") {
				params["diagnostics"] = []wireDiagnostic{{Range: wireRange{wirePosition{0, 0}, wirePosition{0, 1}}, Severity: 1, Message: "fixture error"}}
			}
			_ = c.Notify(ctx, "textDocument/publishDiagnostics", params)
		case "textDocument/documentSymbol":
			outlineRequests++
			if mode == "warmup" && outlineRequests == 1 {
				return []any{}, nil
			}
			if mode == "warmup" && outlineRequests == 2 {
				return nil, &jsonrpc2.Error{Code: -32801, Message: "content modified"}
			}
			return []any{map[string]any{"name": "Alpha", "kind": 12, "range": wireRange{wirePosition{0, 0}, wirePosition{0, 5}}, "selectionRange": wireRange{wirePosition{0, 0}, wirePosition{0, 5}}}}, nil
		case "textDocument/hover":
			cancel := make(chan struct{})
			mu.Lock()
			cancels[r.ID] = cancel
			mu.Unlock()
			// Return handling to the read loop before sending synchronous callbacks.
			go func() {
				var settings []any
				err := c.Call(ctx, "workspace/configuration", map[string]any{"items": []any{map[string]string{"section": "fixture.option"}}}, &settings)
				if err != nil {
					return
				}
				var edit struct{ Applied bool }
				_ = c.Call(ctx, "workspace/applyEdit", map[string]any{"edit": map[string]any{"changes": map[string]any{}}}, &edit)
				trace("callback-results", map[string]any{"settings": settings, "applied": edit.Applied})
				if mode == "cancel" {
					<-cancel
					_ = c.ReplyWithError(ctx, r.ID, &jsonrpc2.Error{Code: -32800, Message: "cancelled"})
					return
				}
				if mode == "hang" {
					select {}
				}
				if mode == "mutate" {
					path, _ := uriPath(stringValue(p["textDocument"], "uri"))
					_ = os.WriteFile(path, []byte("Other\n"), 0600)
				}
				_ = c.Reply(ctx, r.ID, map[string]any{"contents": "Alpha docs", "range": wireRange{wirePosition{0, 0}, wirePosition{0, 5}}})
			}()
			return nil, errDeferred
		case "$/cancelRequest":
			var id jsonrpc2.ID
			_ = json.Unmarshal(p["id"], &id)
			mu.Lock()
			if ch := cancels[id]; ch != nil {
				close(ch)
				delete(cancels, id)
			}
			mu.Unlock()
		case "shutdown":
			return nil, nil
		case "exit":
			os.Exit(0)
		}
		if r.Notif {
			return nil, errDeferred
		}
		return nil, nil
	})
	c := jsonrpc2.NewConn(context.Background(), jsonrpc2.NewBufferedStream(stdioFixture{}, boundedCodec{8 << 20}), handler)
	<-c.DisconnectNotify()
	os.Exit(0)
}
func stringValue(raw json.RawMessage, key string) string {
	var v map[string]string
	_ = json.Unmarshal(raw, &v)
	return v[key]
}

func fakeConfig(t *testing.T, mode string) (Config, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	put(t, filepath.Join(root, "f.go"), "Alpha\n")
	c := Config{Dir: root, DiagnosticTimeoutMS: 100, Servers: []ServerConfig{{ID: "fixture", Command: []string{exe, "-test.run=^TestLanguageServerProcess$"}, Languages: map[string]string{".go": "go"}, Roots: []string{"."}, Env: map[string]string{"STRAP_LSP_FIXTURE": "1", "STRAP_LSP_TRACE": trace, "STRAP_LSP_MODE": mode}, Settings: json.RawMessage(`{"fixture":{"option":"configured"}}`)}}}
	return c, trace
}

func TestColdStartRetriesEmptySemanticResults(t *testing.T) {
	c, trace := fakeConfig(t, "warmup")
	m := manager(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	page, err := m.Outline(ctx, OutlineQuery{Path: "f.go"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "Alpha" || page.Metadata.Partial {
		t.Fatalf("cold outline: %+v %v", page, err)
	}
	if got := traceCount(t, trace, "textDocument/documentSymbol"); got != 3 {
		t.Fatalf("expected empty and invalidated read retries, got %d requests", got)
	}
}
func traceCount(t *testing.T, path, method string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), `"method":"`+method+`"`)
}

func TestSharedProcessCallbacksAndRestart(t *testing.T) {
	c, trace := fakeConfig(t, "")
	m := manager(t, c)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Inspect(context.Background(), InspectQuery{Target: Target{Path: "f.go", Line: 1, Column: 1}})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := traceCount(t, trace, "initialize"); n != 1 {
		t.Fatalf("started %d peers", n)
	}
	raw, _ := os.ReadFile(trace)
	if !strings.Contains(string(raw), `"applied":false`) || !strings.Contains(string(raw), `"settings":["configured"]`) {
		t.Fatalf("callbacks failed: %s", raw)
	}
	outline, err := m.Outline(context.Background(), OutlineQuery{Path: "f.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	var old *client
	for _, s := range m.instances {
		old = s.client
		old.abort()
	}
	m.release()
	select {
	case <-old.done:
	case <-time.After(3 * time.Second):
		t.Fatal("crashed process not reaped")
	}
	if _, err = m.Inspect(context.Background(), InspectQuery{Target: Target{Ref: outline.Items[0].Ref}}); err == nil {
		t.Fatal("accepted dead-generation ref")
	}
	if _, err = m.Outline(context.Background(), OutlineQuery{Path: "f.go"}); err != nil {
		t.Fatal(err)
	}
	if n := traceCount(t, trace, "initialize"); n != 2 {
		t.Fatalf("restart: %d", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err = m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Status(context.Background(), ""); err == nil {
		t.Fatal("query accepted after close")
	}
}
func TestCancellationAndMalformedPeer(t *testing.T) {
	for _, mode := range []string{"cancel", "hang", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			c, trace := fakeConfig(t, mode)
			m := manager(t, c)
			if mode == "malformed" {
				if _, err := m.Outline(context.Background(), OutlineQuery{Path: "f.go"}); err == nil {
					t.Fatal("accepted malformed peer")
				}
				return
			}
			if _, err := m.Outline(context.Background(), OutlineQuery{Path: "f.go"}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := m.Inspect(ctx, InspectQuery{Target: Target{Path: "f.go", Line: 1, Column: 1}})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancel: %v", err)
			}
			if traceCount(t, trace, "$/cancelRequest") != 1 {
				t.Fatal("missing protocol cancellation")
			}
			if _, err = m.Outline(context.Background(), OutlineQuery{Path: "f.go"}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "hang" {
				want = 2
			}
			if n := traceCount(t, trace, "initialize"); n != want {
				t.Fatalf("starts %d want %d", n, want)
			}
		})
	}
}
func TestSynchronizationAndDiagnosticFreshness(t *testing.T) {
	for _, mode := range []string{"", "versionless", "mutate"} {
		t.Run(mode, func(t *testing.T) {
			c, trace := fakeConfig(t, mode)
			m := manager(t, c)
			outline, err := m.Outline(context.Background(), OutlineQuery{Path: "f.go"})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "mutate" {
				got, err := m.Inspect(context.Background(), InspectQuery{Target: Target{Ref: outline.Items[0].Ref}})
				if err != nil {
					t.Fatal(err)
				}
				if got.Metadata.Freshness != "stale" {
					t.Fatalf("silently rebound to changed source: %+v", got)
				}
				if _, err = m.Inspect(context.Background(), InspectQuery{Target: Target{Ref: got.Location.Ref}}); err == nil {
					t.Fatal("accepted ref after concurrent edit")
				}
				return
			}
			path := filepath.Join(c.Dir, "f.go")
			put(t, path, "BROKEN\n")
			m.Changed(path)
			page, err := m.Diagnostics(context.Background(), DiagnosticQuery{Paths: []string{"f.go"}})
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("changed diagnostics: %+v %v", page, err)
			}
			if mode == "versionless" && (page.Metadata.Freshness == "synchronized" || page.Items[0].Ref != "") {
				t.Fatal("versionless is not synchronized")
			}
			if traceCount(t, trace, "textDocument/didChange") < 1 {
				t.Fatal("missing incremental sync")
			}
			put(t, path, "Alpha\n")
			m.Changed(path)
			page, err = m.Diagnostics(context.Background(), DiagnosticQuery{Paths: []string{"f.go"}})
			if err != nil || len(page.Items) != 0 {
				t.Fatalf("clear: %+v %v", page, err)
			}
			if len(page.Metadata.Checks) != 1 {
				t.Fatal("missing completion status")
			}
		})
	}
}

var errDeferred = errors.New("deferred fixture response")

type fixtureHandler func(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request) (any, error)

func (h fixtureHandler) Handle(ctx context.Context, c *jsonrpc2.Conn, r *jsonrpc2.Request) {
	v, err := h(ctx, c, r)
	if r.Notif || errors.Is(err, errDeferred) {
		return
	}
	if err != nil {
		var rpc *jsonrpc2.Error
		if !errors.As(err, &rpc) {
			rpc = &jsonrpc2.Error{Code: -32603, Message: err.Error()}
		}
		_ = c.ReplyWithError(ctx, r.ID, rpc)
	} else {
		_ = c.Reply(ctx, r.ID, v)
	}
}
