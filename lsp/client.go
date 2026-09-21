package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sourcegraph/jsonrpc2"
	"github.com/stevemurr/strap/internal/webprocess"
)

// boundedCodec adds allocation/header bounds to the transport's LSP codec.
// JSON-RPC dispatch, request IDs, response routing and connection ownership remain
// the transport's responsibility.
type boundedCodec struct{ limit int }

func (c boundedCodec) WriteObject(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > c.limit {
		return fmt.Errorf("LSP message exceeds byte limit")
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(b))
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func (c boundedCodec) ReadObject(r *bufio.Reader, v any) error {
	n := -1
	total := 0
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return err
		}
		total += len(line)
		if total > 16384 {
			return fmt.Errorf("LSP headers exceed limit")
		}
		if len(line) < 2 || line[len(line)-2] != '\r' {
			return fmt.Errorf("invalid LSP header terminator")
		}
		s := string(line[:len(line)-2])
		if s == "" {
			break
		}
		key, value, ok := strings.Cut(s, ":")
		if !ok {
			return fmt.Errorf("invalid LSP header")
		}
		if strings.EqualFold(key, "Content-Length") {
			if n >= 0 {
				return fmt.Errorf("duplicate Content-Length")
			}
			n, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 1 || n > c.limit {
				return fmt.Errorf("invalid or oversized LSP Content-Length")
			}
		}
	}
	if n < 1 {
		return fmt.Errorf("missing LSP Content-Length")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

type pipeStream struct {
	r, w *os.File
	once sync.Once
}

func (p *pipeStream) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *pipeStream) Write(b []byte) (int, error) {
	if err := p.w.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return 0, err
	}
	return p.w.Write(b)
}
func (p *pipeStream) Close() error {
	p.once.Do(func() { _ = p.r.Close(); _ = p.w.Close() })
	return nil
}

type wireDiagnostic struct {
	Range    wireRange       `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code"`
	Source   string          `json:"source"`
	Message  string          `json:"message"`
}
type diagnosticSet struct {
	Version  *int
	Items    []wireDiagnostic
	Received time.Time
	Epoch    uint64
	Revision uint64 // Pull diagnostic refresh generation; push producers are independent.
}
type client struct {
	requestTimeout            time.Duration
	warmupUntil               time.Time
	startupIncomplete         bool
	progress                  map[string]bool
	conn                      *jsonrpc2.Conn
	cmd                       *exec.Cmd
	stream                    *pipeStream
	done                      chan struct{}
	stderr                    *webprocess.Buffer
	seq                       atomic.Uint64
	epoch                     atomic.Uint64
	mu                        sync.Mutex // Callback-owned data never takes the manager gate.
	diagnostics               map[string]diagnosticSet
	pulledDiagnostics         map[string]diagnosticSet
	diagnosticRevision        atomic.Uint64
	refresh                   chan<- string
	diagnosticBytes           int
	diagnosticLimit           int
	diagnosticGap             bool
	changed                   chan struct{}
	registrations             map[string][]fileWatcher
	caps                      map[string]json.RawMessage
	encoding                  string
	syncKind                  int
	openClose, save, saveText bool
	version                   string
	spec                      LaunchSpec
}

func startClient(ctx context.Context, spec LaunchSpec, cfg Config, refresh chan<- string) (*client, error) {
	cmd := exec.Command(spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	// Configure process groups with the same platform handling as existing tools.
	webprocess.Configure(cmd)
	cmd.Cancel = nil
	input, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	reader, output, err := os.Pipe()
	if err != nil {
		input.Close()
		writer.Close()
		return nil, err
	}
	c := &client{refresh: refresh, progress: map[string]bool{}, requestTimeout: milliseconds(cfg.RequestTimeoutMS), cmd: cmd, stream: &pipeStream{r: reader, w: writer}, done: make(chan struct{}), stderr: &webprocess.Buffer{Limit: 8192}, diagnostics: map[string]diagnosticSet{}, pulledDiagnostics: map[string]diagnosticSet{}, diagnosticLimit: cfg.CacheBytes, changed: make(chan struct{}, 1), registrations: map[string][]fileWatcher{}, encoding: "utf-16", spec: spec}
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = c.stderr
	cmd.WaitDelay = 250 * time.Millisecond
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		c.stream.Close()
		return nil, failure("server_unavailable", "start language server: %v", err)
	}
	input.Close()
	output.Close()
	go func() { _ = cmd.Wait(); _ = webprocess.Kill(cmd); c.stream.Close(); close(c.done) }()
	c.conn = jsonrpc2.NewConn(context.Background(), jsonrpc2.NewBufferedStream(c.stream, boundedCodec{cfg.MaxMessageBytes}), c, jsonrpc2.SetLogger(log.New(c.stderr, "", 0)))
	ready := false
	defer func() {
		if !ready {
			c.abort()
			<-c.done
		}
	}()

	var result struct {
		Capabilities map[string]json.RawMessage     `json:"capabilities"`
		ServerInfo   struct{ Name, Version string } `json:"serverInfo"`
	}
	capabilities := map[string]any{
		"window":       map[string]any{"workDoneProgress": true},
		"general":      map[string]any{"positionEncodings": []string{"utf-8", "utf-16", "utf-32"}},
		"workspace":    map[string]any{"configuration": true, "workspaceFolders": true, "applyEdit": false, "diagnostics": map[string]any{"refreshSupport": true}, "didChangeWatchedFiles": map[string]any{"dynamicRegistration": true}},
		"textDocument": map[string]any{"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true}, "definition": map[string]any{"linkSupport": true}, "declaration": map[string]any{"linkSupport": true}, "typeDefinition": map[string]any{"linkSupport": true}, "implementation": map[string]any{"linkSupport": true}, "hover": map[string]any{"contentFormat": []string{"markdown", "plaintext"}}, "publishDiagnostics": map[string]any{"versionSupport": true}, "diagnostic": map[string]any{"dynamicRegistration": false, "relatedDocumentSupport": false}},
	}
	params := map[string]any{"processId": os.Getpid(), "clientInfo": map[string]string{"name": "strap"}, "rootUri": fileURI(spec.Root), "workspaceFolders": []any{map[string]string{"uri": fileURI(spec.Root), "name": spec.Root}}, "capabilities": capabilities}
	if len(spec.InitializationOptions) > 0 {
		params["initializationOptions"] = spec.InitializationOptions
	}
	if err = c.call(ctx, "initialize", params, &result); err != nil {
		c.abort()
		return nil, err
	}
	c.caps = result.Capabilities
	version := result.ServerInfo.Version
	var build struct{ Version string }
	if json.Unmarshal([]byte(version), &build) == nil && build.Version != "" {
		version = build.Version
	}
	c.version = cut(strings.TrimSpace(result.ServerInfo.Name+" "+version), 256)
	if raw := c.caps["positionEncoding"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &c.encoding); err != nil {
			c.abort()
			return nil, err
		}
	}
	if c.encoding != "utf-8" && c.encoding != "utf-16" && c.encoding != "utf-32" {
		c.abort()
		return nil, failure("unsupported", "server position encoding %s", c.encoding)
	}
	if raw := c.caps["textDocumentSync"]; len(raw) > 0 {
		if json.Unmarshal(raw, &c.syncKind) == nil {
			c.openClose = c.syncKind != 0
		} else {
			var syncOptions struct {
				OpenClose bool            `json:"openClose"`
				Change    int             `json:"change"`
				Save      json.RawMessage `json:"save"`
			}
			if err = json.Unmarshal(raw, &syncOptions); err != nil {
				c.abort()
				return nil, err
			}
			c.syncKind = syncOptions.Change
			c.openClose = syncOptions.OpenClose
			c.save = len(syncOptions.Save) > 0 && string(syncOptions.Save) != "false" && string(syncOptions.Save) != "null"
			var save struct {
				IncludeText bool `json:"includeText"`
			}
			_ = json.Unmarshal(syncOptions.Save, &save)
			c.saveText = save.IncludeText
		}
	}
	if err = c.notify(ctx, "initialized", struct{}{}); err != nil {
		c.abort()
		return nil, err
	}
	if len(spec.Settings) > 0 {
		if err = c.notify(ctx, "workspace/didChangeConfiguration", map[string]any{"settings": spec.Settings}); err != nil {
			c.abort()
			return nil, err
		}
	}
	c.warmupUntil = time.Now().Add(min(5*time.Second, milliseconds(cfg.StartTimeoutMS)))
	ready = true
	return c, nil
}

func (c *client) supported(cap string) bool {
	v := c.caps[cap]
	return len(v) > 0 && string(v) != "false" && string(v) != "null"
}
func (c *client) alive() bool {
	select {
	case <-c.conn.DisconnectNotify():
		return false
	default:
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.conn.Notify(ctx, method, params)
}
func (c *client) call(ctx context.Context, method string, params, result any) error {
	if method != "initialize" && method != "shutdown" && c.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.requestTimeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := jsonrpc2.ID{Num: c.seq.Add(1)}
	waiter, err := c.conn.DispatchCall(ctx, method, params, jsonrpc2.PickID(id))
	if err != nil {
		return failure("server_error", "%s: %v", method, err)
	}
	err = waiter.Wait(ctx, result)
	if ctx.Err() != nil {
		_ = c.conn.Notify(context.Background(), "$/cancelRequest", map[string]any{"id": id.Num})
		// A compliant peer still answers a cancelled request. Bound abandoned
		// transport pending entries if the server stops answering altogether.
		grace, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		if e := waiter.Wait(grace, nil); errors.Is(e, context.DeadlineExceeded) {
			c.abort()
		}
		return ctx.Err()
	}
	if err != nil {
		var rpc *jsonrpc2.Error
		if errors.As(err, &rpc) {
			switch rpc.Code {
			case jsonrpc2.CodeMethodNotFound:
				return failure("unsupported", "%s is unavailable", method)
			case -32801: // LSP ContentModified: server snapshot changed during the read.
				return failure("content_modified", "%s: %s", method, rpc.Message)
			}
		}
		return failure("server_error", "%s: %v", method, err)
	}
	return nil
}

func (c *client) abort() { c.stream.Close(); _ = webprocess.Kill(c.cmd); _ = c.conn.Close() }
func (c *client) close(ctx context.Context) error {
	if c.alive() {
		grace, cancel := context.WithTimeout(ctx, time.Second)
		_ = c.call(grace, "shutdown", nil, nil)
		_ = c.notify(grace, "exit", nil)
		cancel()
	}
	select {
	case <-c.done:
		c.stream.Close()
		_ = c.conn.Close()
		return nil
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
	}
	c.abort()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Handle never waits for a semantic request, nor acquires the manager gate.
// Callback replies are bounded by the pipe's write deadline.
func (c *client) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var result any
	switch req.Method {
	case "workspace/configuration":
		var p struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		if len(p.Items) > 128 {
			if !req.Notif {
				_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: "configuration item limit exceeded"})
			}
			return
		}
		var settings any
		_ = json.Unmarshal(c.spec.Settings, &settings)
		items := make([]any, len(p.Items))
		for i, item := range p.Items {
			v := settings
			if item.Section != "" {
				for _, key := range strings.Split(item.Section, ".") {
					if m, ok := v.(map[string]any); ok {
						v = m[key]
					} else {
						v = nil
						break
					}
				}
			}
			items[i] = v
		}
		result = items
	case "workspace/workspaceFolders":
		result = []any{map[string]string{"uri": fileURI(c.spec.Root), "name": c.spec.Root}}
	case "workspace/applyEdit":
		result = map[string]any{"applied": false, "failureReason": "Strap LSP tools are read-only"}
	case "window/showMessageRequest":
		result = nil
	case "window/workDoneProgress/create":
		result = nil
	case "client/registerCapability":
		var p struct {
			Registrations []registration `json:"registrations"`
		}
		var err error
		if req.Params == nil {
			err = fmt.Errorf("missing registration parameters")
		} else {
			err = json.Unmarshal(*req.Params, &p)
		}
		if err == nil {
			err = c.register(p.Registrations)
		}
		if err != nil {
			if !req.Notif {
				_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: err.Error()})
			}
			return
		}
	case "client/unregisterCapability":
		var p struct {
			Unregistrations []struct{ ID string } `json:"unregisterations"`
		}
		if req.Params != nil {
			_ = json.Unmarshal(*req.Params, &p)
		}
		c.mu.Lock()
		for _, r := range p.Unregistrations {
			delete(c.registrations, r.ID)
		}
		c.mu.Unlock()
	case "textDocument/publishDiagnostics":
		var p struct {
			URI         string           `json:"uri"`
			Version     *int             `json:"version"`
			Diagnostics []wireDiagnostic `json:"diagnostics"`
		}
		if req.Params == nil || json.Unmarshal(*req.Params, &p) != nil {
			return
		}
		c.storeDiagnostics(p.URI, diagnosticSet{Version: p.Version, Items: p.Diagnostics, Received: time.Now(), Epoch: c.epoch.Load()})
		select {
		case c.changed <- struct{}{}:
		default:
		}
		return
	case "workspace/diagnostic/refresh":
		c.diagnosticRevision.Add(1)
		// Invalidate cached pulls immediately and schedule a bounded refresh of
		// open documents without blocking this callback on the manager gate.
		select {
		case c.refresh <- "":
		default:
		}
		select {
		case c.changed <- struct{}{}:
		default:
		}
	case "$/progress":
		var p struct {
			Token json.RawMessage `json:"token"`
			Value struct {
				Kind string `json:"kind"`
			} `json:"value"`
		}
		if req.Params != nil && json.Unmarshal(*req.Params, &p) == nil {
			key := string(p.Token)
			c.mu.Lock()
			if p.Value.Kind == "begin" && len(c.progress) < 128 {
				c.progress[key] = true
			}
			if p.Value.Kind == "end" {
				delete(c.progress, key)
			}
			c.mu.Unlock()
			select {
			case c.changed <- struct{}{}:
			default:
			}
		}
		return
	case "window/logMessage", "window/showMessage", "telemetry/event":
		return
	default:
		if !req.Notif {
			_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "unsupported client method"})
		}
		return
	}
	if !req.Notif {
		_ = conn.Reply(ctx, req.ID, result)
	}
}
