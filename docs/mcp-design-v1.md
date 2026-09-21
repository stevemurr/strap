# MCP (Model Context Protocol) Support Design — v1

> Status: proposal. Not yet implemented.

## Motivation

Strap currently exposes tools to agents from a fixed set of builtin types
(shell, files, PDF), dynamically generated LSP tools, workflow management
tools, and optional web tools. Adding **MCP client support** lets any agent
invoke external tool servers discovered at session startup — for example,
Fastmail email/calendar via JMAP, filesystem browsing, or search services —
without changing the core harness contract.

An MCP-wrapped tool is indistinguishable from a builtin local tool from the
LLM's perspective: same `tool.Tool` interface, same injection path into the
role's toolset, same result flow back through conversation history.

The protocol spec in force is MCP revision 2026-07-28, authored by a Series
of LF Projects, LLC. See <https://modelcontextprotocol.io/specification/2026-07-28>.

---

## Scope

### In scope

- Read server configuration from an existing strap config file (JSON).
- Spawn each declared MCP server as a subprocess (stdio transport only).
- Discover tools via `tools/list`, catalog them at startup.
- Translate each MCP tool definition → a `tool.Tool` implementation.
- Wire tool invocations to JSON-RPC `tools/call` over stdio.
- Inject discovered tools into the session's tool list alongside builtin
  local tools; wrap in existing role assignment (`VisibleTo`).
- Manage server process lifecycle (graceful shutdown on session close).

### Out of scope (Phase 1)

- Streamable HTTP transport for MCP servers.
- Server mode (exposing strap tools via MCP).
- Streaming / multi-round-trip tool results.
- Resources, prompts, subscriptions/features beyond `tools/list` +
  `tools/call`.
- Auto-discovery of installed MCP servers.
- SSE transport for remote MCP endpoints.

---

## Wire Format Overview

MCP uses **JSON-RPC 2.0** as its message encoding. The wire structure is
identical across transports; only the delivery mechanism differs.

A request:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "search_emails",
    "arguments": { "limit": 5 },
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientInfo": { "name": "strap", "version": "dev" },
      "io.modelcontextprotocol/clientCapabilities": {}
    }
  }
}
```

A success response:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "resultType": "complete",
    "content": [{ "type": "text", "text": "..." }],
    "isError": false
  }
}
```

On **stdio**, messages are newline-delimited UTF-8 strings, one per line,
sent over stdin/stdout pipes of the child process. On **Streamable HTTP**
(not included in Phase 1), each message is a standalone HTTP POST to a
single MCP endpoint with headers mirroring some `_meta` fields.

The two transports share the identical JSON-RPC body format.

---

## Architecture

```
┌─────────────────────────────────────────────┐
│                  Config File                 │
│       ~/.config/strap/mcp_servers.json       │
│                                             │
│   { "mcp": {                               │
│     "servers": [                            │
│       { "name": "fastmail", ... },          │
│       { "name": "filesystem", ... }         │
│     ]                                       │
│   }}                                        │
└──────────────────┬──────────────────────────┘
                   │ parsed into harness.Config.MCP
                   ▼
┌─────────────────────────────────────────────┐
│             harness.New()                    │
│                                              │
│  For each server config:                     │
│    ┌──────────────────────────────────────┐  │
│    │  tool/mcp.Server                      │  │
│    │                                      │  │
│    │  1. Spawn subprocess                 │  │
│    │  2. discover() / version negotiate   │  │
│    │  3. listTools() → tools/list call    │  │
│    │  4. Convert each MCP tool def        │  │
│    │     → tool.Tool wrapper              │  │
│    │                                      │  │
│    │  Internal state:                     │  │
│    │    - *exec.Cmd (subprocess)          │  │
│    │    - stdin pipe (writes requests)    │  │
│    │    - stdout pipe (reads responses)   │  │
│    │    - readLoop goroutine (dispatch)   │  │
│    │    - pending map (in-flight calls)   │  │
│    │    - tools []cached definitions      │  │
│    └──────────────────────────────────────┘  │
│                   │                          │
│                   │ wraps[]tool.Tool         │  │
│                   ▼                          │  │
│  ┌─────────────────────────────────────┐    │
│  │  local tool list (appended here)    │    │
│  │  shell · files · pdf · lsp · web    │    │
│  │  fastmail/search_emails             │    │
│  │  fastmail/send_email                │    │
│  │  filesystem/read_file               │    │
│  │  workflow/create_plan (etc.)        │    │
│  └─────────────────────────────────────┘    │
└─────────────────────────────────────────────┘
```

---

## Configuration Types

Added to `harness/session.go` alongside `WebConfig`, `lsp.Config`, etc.

```go
type MCPConfig struct {
    Servers []MCPClientConfig `json:"servers,omitempty"`
}

type MCPClientConfig struct {
    // Name is a unique server identifier used for tool name disambiguation.
    // Tools exposed by this server will be prefixed as "servername/toolname"
    // so that cross-server collisions are impossible.
    Name string `json:"name"`

    // Command is the executable to run (e.g., "npx", "uvx", an absolute path).
    // Must be findable on PATH or be an absolute path.
    Command string `json:"command"`

    // Args are passed verbatim after Command. Typical:
    //   ["-y", "@jmhron/fastmail-mcp"]
    Args []string `json:"args,omitempty"`

    // Env overrides inherited from the parent process. Keys override; unset
    // keys fall through to the host environment. Nil/empty means inherit all.
    Env map[string]string `json:"env,omitempty"`

    // Timeout caps how long any single tools/call may take. Zero uses the
    // session model timeout (config.Model.Timeout).
    Timeout time.Duration `json:"timeout,omitempty"`

    // VisibleTo controls which agent roles receive these tools. Roles not
    // listed will never see the tools even though the server runs and
    // contributes to effective-config reporting. Nil/empty means all roles.
    VisibleTo []string `json:"visible_to,omitempty"`
    // Possible values: "root", "implementor", "auditor", "researcher"
}
```

Example user config (`~/.config/strap/mcp_servers.json` merged into main):

```jsonc
{
  "servers": [
    {
      "name": "fastmail",
      "command": "npx",
      "args": ["-y", "@jmhron/fastmail-mcp"],
      "env": { "FASTMAIL_TOKEN": "fm_xxxxxxxx" },
      "timeout": "10s",
      "visible_to": ["root", "researcher"]
    },
    {
      "name": "filesystem",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/projects"],
      "timeout": "30s"
    }
  ]
}
```

The CLI merges this into `Config.MCP.Servers` during options parsing.

---

## Tool Wrapping Strategy

Each MCP tool discovered via `tools/list` becomes a thin `tool.Func[A]`
implementing strap's `Tool` interface:

```go
func (f Func[A]) Definition() provider.ToolDefinition
func (f Func[A]) Call(ctx context.Context, call Call) (Result, error)
```

### Name Resolution

- MCP tool names become `serverName/originalToolName` (e.g.,
  `fastmail/search_emails`).
- This prevents collision when multiple servers expose a `search` tool.
- Descriptions are prefixed similarly: `fastmail/: Original title — description.`

### Argument Contract

- MCP tool `inputSchema` (a JSON Schema object) is translated into
  strap's typed `Parameters[A]` using the existing parameters system.
- Validation happens before the RPC round-trip, matching existing behavior.
- `tool.Call.Arguments` carries the validated JSON to the handler.

### Invocation Flow

```
LLM generates tool call → name="fastmail/search_emails" args={...}
    ↓
harness dispatches to wrapped tool.Func
    ↓
Handler serializes → tools/call JSON-RPC request
    ├─ adds required _meta (protocolVersion, clientInfo, clientCapabilities)
    ├─ writes to server stdin with trailing newline
    ↓
Server goroutine (readLoop) receives response line from stdout
    ├─ correlates by JSON-RPC id
    ├─ unmarshals result → mcpResult struct
    ↓
Wrapper converts MCP content array [{type:"text",text:"..."}] →
    strap content.Content [{Part{Text:"..."}}]
    ↓
Returns tool.Result{Content: ...} to harness agent loop
```

### Result Conversion Rules

| MCP Content Type | Strap Mapping |
|---|---|
| `{type:"text", text:"..."}` | `content.Part{Text: "..."}` |
| `{type:"image", data:"base64", mimeType:"image/png"}` | `content.Part{Image: &content.Image{...}}` |
| `{type:"audio", ...}` | Truncated / text summary (not supported by current `content.Image` MIME restrictions) |
| `{type:"resource_link", ...}` | Serialized as text (tool returns the link URI) |
| `{type:"resource", ...}` | Embedded resource text extracted as text part |
| `structuredContent` field | Text-serialized alongside text part for compatibility |
| `resultType: "input_required"` | Handled via MRTR retry logic (future phase) |

### Error Handling

| Condition | Behavior |
|---|---|
| Missing binary / cannot spawn | Startup error, session fails (unless `skip_on_error` flag is added later) |
| Protocol handshake fails | Startup error |
| Unknown tool name called | JSON-RPC error `-32602` (Invalid params) → surfaced as tool error |
| Server crashes mid-session | `readLoop` detects EOF → aborts pending calls with `io.ErrUnexpectedEOF` |
| Server errors (isError:true) | Tool execution error (LLM can self-correct) |
| Timeout exceeded | Cancelled context → error returned to caller |

---

## Transport Layer: `tool/mcp.Server`

The new package lives at `tool/mcp/server.go`. It owns the full MCP
communication cycle for one server subprocess.

### Public Interface

```go
package mcp

// Server wraps one MCP server subprocess and exposes its tools as strap
// tool.Tool implementations. Use New() to construct.
type Server struct { /* internal */ }

// New starts the subprocess, discovers available features, catalogs tools,
// and returns a ready-to-use Server. If startup fails, it closes the
// resource handle. Never spawns browser or initiates network calls.
func New(cfg MCPClientConfig) (*Server, error)

// Close shuts down the server: close stdin, wait up to 5s for clean exit,
// SIGTERM, SIGKILL if needed. Safe to call multiple times (idempotent).
func (s *Server) Close(ctx context.Context) error

// Tools returns slice-of-tool.Tool wrappers for every discovered MCP tool.
// Each wrapper delegates Call() to the server via tools/call JSON-RPC.
// Names are prefixed as s.name + "/" + originalToolName.
func (s *Server) Tools() []tool.Tool
```

### Internal State

```go
type Server struct {
    name    string
    cmd     *exec.Cmd
    stdin   io.WriteCloser    // pipe to child stdin (requests)
    stdout  io.ReadCloser     // pipe from child stdout (responses)
    ctx     context.Context   // cancelled on Close()
    cancel  context.CancelFunc
    wg      sync.WaitGroup    // tracks readLoop goroutine
    pending map[int64]*callPending  // inflight requests keyed by JSON-RPC id
    mu      sync.Mutex
    nextID  int64
    timeout time.Duration
    tools   []mcpToolDef      // cached from tools/list
    closed  bool
}

type callPending struct {
    replyCh chan mcpResult
}

type mcpToolDef struct {
    name        string
    title       string
    description string
    inputSchema json.RawMessage
}

type mcpResult struct {
    resultType  string          // "complete" or "input_required"
    content     []json.RawMessage // MCP content items (text/image/resource)
    isError     bool
    structured  json.RawMessage // optional structured result data
}
```

### Request Sending

Every tool invocation routes through a single method:

```go
func (s *Server) request(ctx context.Context, method string, params any) (mcpResult, error)
```

This method:
1. Allocates a monotonically increasing JSON-RPC `id`.
2. Serializes the request body with required `_meta` fields.
3. Writes `body + \n` to `stdin`.
4. Creates a channel-based pending entry.
5. Blocks (selecting on ctx.Done, timeout timer, or replyCh) until
   the response arrives or the context cancels.
6. Removes the pending entry.

If `ctx` is cancelled or timeout fires, sends `notifications/cancelled`
before returning.

### Read Loop (Background Goroutine)

Runs continuously for the server's lifetime:

```go
func (s *Server) readLoop(r io.Reader) {
    defer s.wg.Done()
    scanner := bufio.NewScanner(r)
    for scanner.Scan() {
        line := scanner.Text() // one newline-delimited JSON-RPC message
        var msg jsonrpcEnvelope
        if err := json.Unmarshal([]byte(line), &msg); err != nil {
            continue // malformed, drop silently
        }
        switch {
        case msg.ID != nil && msg.Error != nil:
            deliverResponse(msg.ID, mcpResult{}, &msg.Error)
        case msg.ID != nil && msg.Result != nil:
            var res mcpResult
            if err := unmarshalResult(msg.Result, &res); err != nil {
                deliverResponseError(msg.ID, err)
            } else {
                deliverResponse(msg.ID, res, nil)
            }
        case msg.ID == nil && msg.Method != "":
            handleNotification(msg.Method, msg.Params)
        }
    }
    // Scanner exhausted → server exited abnormally
    s.abortAll(io.ErrUnexpectedEOF)
}
```

Notifications handled include `notifications/progress` (forwarded to the
waiting call's context), `notifications/tools/list_changed` (cached for
future invalidation — Phase 2), and others.

### Lifecycle / Shutdown Sequence

```go
func (s *Server) Close(ctx context.Context) error {
    s.mu.Lock()
    if s.closed { s.mu.Unlock(); return nil }
    s.closed = true
    s.cancel()
    s.mu.Unlock()

    // Step 1: Close stdin (server SHOULD exit on EOF)
    s.stdin.Close()

    // Step 2: Wait up to 5 seconds
    done := make(chan struct{})
    go func() { s.cmd.Wait(); close(done) }()
    select {
    case <-done:
        return nil
    case <-time.After(5 * time.Second):
        // Step 3: SIGTERM
        s.cmd.Process.Signal(syscall.SIGTERM)
        select {
        case <-done:
            return nil
        case <-time.After(3 * time.Second):
            // Step 4: SIGKILL (force)
            s.cmd.Process.Kill()
            s.cmd.Wait()
            return nil
        }
    }
}
```

Called from `session.Dispose()` via the `resource.Group` registration.

---

## Startup Wiring (`harness/session.go`)

In `New()`, approximately where web tools are assembled (around line ~330):

```go
var mcpServers []*mcp.Server

if cfg.MCP != nil && len(cfg.MCP.Servers) > 0 {
    for _, srvCfg := range cfg.MCP.Servers {
        srv, err := mcp.New(srvCfg)
        if err != nil {
            return nil, fmt.Errorf("mcp server %q: %w", srvCfg.Name, err)
        }
        s.resources.Add("mcp:"+srvCfg.Name, srv)
        mcpServers = append(mcpServers, srv)
    }

    for i, srv := range mcpServers {
        tools := srv.Tools()
        for j, t := range tools {
            // Apply role filter: skip tools not visible to this role
            if !isVisibleTo(t, roleFilter) {
                // Exclude from this role's toolset
                // (role filtering happens later per-role;
                // all tools are injected into local, then filtered during Spec assembly)
                _ = i; _ = j
            }
        }
        local = append(local, tools...)
    }
}
```

Tools appear in the same `local` tool slice as shell/files/PDF/web, which
are then distributed to roles via `agent.Spec.Tools` with the same role
filtering applied as other builtin tools.

---

## Error Handling Policy

| Failure Mode | Session Behavior |
|---|---|
| One MCP server cannot start (missing binary, bad command) | Log warning, skip server. Other servers continue. Session starts normally. |
| Protocol mismatch (server doesn't speak modern MCP) | Log warning, skip server. |
| tools/list fails after connect | Log warning, server still starts but has zero tools. |
| Server crashes after successful start | Pending calls fail with error; server auto-restarted on next invocation. (Phase 2) |
| Server responds but tool call fails | Returned as tool execution error (isError=true); session continues. |
| All MCP servers fail to start | Session continues with no MCP tools loaded. No failure propagated to caller. |

This "fail-silent-per-server" policy keeps individual misconfigured MCP
servers from crashing the entire harness session. Users get warnings in
event logs and effective-config descriptions.

---

## Role-Based Visibility

MCP tools are discovered once and stored in the `local` tool list. During
`agent.Spec` assembly (line ~382–398 in `session.go`), each role gets a
dedicated subset:

```go
// Pseudocode for role filtering:
for _, srv := range mcpServers {
    for _, t := range srv.Tools() {
        if !contains(cfg.VisibleTo, role) {
            continue // this role does not receive this server's tools
        }
        roleTools = append(roleTools, t)
    }
}
```

Default (when `VisibleTo` is empty): all four roles receive all MCP tools.
Typical production config might restrict:
- Email/calendar tools to `root` + `researcher` (agents shouldn't modify
  user mailboxes without explicit intent).
- Filesystem tools to all roles (standard automation needs).

---

## Effective Config Reporting

Discovered tools appear in `EffectiveConfig` (returned by
`s.Configuration()`) alongside LSP status and tool contract version.
The MCP section reports:
- Server names and versions from discovery.
- Number of tools discovered per server.
- Total tool count contributed by MCP.

This enables inspection APIs and event-log traceability.

---

## Phased Implementation

### Phase 1 — Core stdio MCP client
Files created:
- `tool/mcp/server.go` — process spawning, stdio transport, JSON-RPC
  framing, `discover()` + `listTools()`, `request()` loop, result→content
  conversion, lifecycle/shutdown.
- `tool/mcp/types.go` — shared types (`mcpToolDef`, `mcpResult`,
  JSON-RPC envelope structs).
- `tool/mcp/server_test.go` — tests against a stub MCP server.
Changes to existing files:
- `harness/session.go` — add `MCPConfig` type to `Config`; wire up in
  `New()` to spawn servers, inject tools, register for cleanup.
- `cmd/strap/options.go` — merge `mcp_servers.json` into `Config.MCP`.
- Add `github.com/modelcontextprotocol/...` Go module dep (if needed for
  shared types — likely none needed since we parse raw JSON directly).

### Phase 2 — Per-server timeout + visibility control
- Add `Timeout` and `VisibleTo` fields to `MCPClientConfig`.
- Apply per-request timeout in `Server.request()`.
- Implement role filter during `agent.Spec` assembly.
- Add `--mcp` flag to CLI for quick enable/disable.

### Phase 3 — Diagnostics and eval helpers
- Log tool registration to event log (same pattern as LSP tools).
- Provide `EvalMCPTestHelper` for integration test stubs.
- Consider tool list change notifications (`tools/list_changed`) caching.

---

## Files Summary

| New Files | Purpose |
|---|---|
| `tool/mcp/server.go` | Process lifecycle, stdio transport, request/response loop |
| `tool/mcp/types.go` | Shared data types, JSON-RPC envelopes, MCP types |
| `tool/mcp/server_test.go` | Tests against stub MCP server |

| Modified Files | Changes |
|---|---|
| `harness/session.go` | Add `MCPConfig` to `Config`; wire servers in `New()` |
| `cmd/strap/options.go` | Parse/merge `mcp_servers.json` into config |

---

## Appendix: JSON-RPC Envelope Structs

Internal structs the transport layer uses:

```go
// jsonrpcEnvelope matches the wire format for both requests and responses.
type jsonrpcEnvelope struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      *int64          `json:"id,omitempty"`
    Method  string          `json:"method,omitempty"`
    Params  json.RawMessage `json:"params,omitempty"`
    Result  json.RawMessage `json:"result,omitempty"`
    Error   *json.RawMessage `json:"error,omitempty"`
}

// jsonrpcRequest is the outgoing shape for a request.
type jsonrpcRequest struct {
    JSONRPC string            `json:"jsonrpc"`
    ID      int64             `json:"id"`
    Method  string            `json:"method"`
    Params  json.RawMessage   `json:"params"`
}
```

These structs are private to `tool/mcp` and never leak into the public
API. They mirror what the spec requires without depending on an external
JSON-RPC library.
