# LSP integration implementation plan

Status: experimental implementation delivered, September 20, 2026. This records
the plan following the [research and tool design](LSP_DESIGN.md). The public
package, seven tools, session ownership, edit hooks, CLI/eval flags, and TUI
presentation are implemented. Go, Rust, Python, JavaScript/TypeScript and Bash
presets are enabled by default and covered by real-server interoperability
fixtures. [Live Qwen3.6 evaluation](../evals/2026-09-20-lsp-reliability.md) measured
tool selection and real-harness traversal. Source targets now use exact
identifiers and host-computed columns after repeated model position errors.
Descriptions clarify handle reuse, path copying and inspection versus navigation.
Broader comparative task evaluation remains release work.
See [shipped behavior and verification](../lsp.md).

The initial implementation consolidates the suggested file layout, serializes
query transactions, and uses bounded file hashing plus watcher hints. Background
diagnostics refresh only already opened documents on running servers; a later
tool result can append a cached summary. These limits are explicit in the user
documentation. Rename, code actions and call hierarchy remain follow-on work.

Create a public `lsp` package that owns the shared client and exposes typed code
queries. Build the seven proposed tools on that package, with one manager owned
by each harness session. Ship Go navigation and diagnostics first. Establish
extensibility with configuration and a small setup adapter, then verify it with a
second real server before treating the extension interface as stable.

**Package and extension boundary**

The external server implements LSP. Strap's client implements protocol operations
once, including initialization, hover, symbols, definition, references, document
synchronization, and diagnostics. Per-server code describes how to select and
launch that server. This follows the protocol's common
[initialization and capability exchange](https://github.com/microsoft/language-server-protocol/blob/gh-pages/_specifications/lsp/3.17/general/initialize.md).

| Layer | Owns |
| --- | --- |
| `lsp.Manager` | Routing, server instances, synchronized documents, normalized queries, references, diagnostic state, and bounded result retention. |
| Private LSP client | JSON-RPC transport, protocol types, capabilities, callbacks, encoding conversion, request cancellation, and process lifecycle. |
| Server configuration | File/language matching, executable and arguments, environment overrides, root rules, initialization options, and settings. |
| `lsp.Adapter` | Optional server-specific workspace and launch resolution. The generic implementation works from configuration. |
| `tool` adapters | Strict model input contracts and rendering bounded results; all languages use the same tools. |
| `harness.Session` | Manager ownership, role tool registration, file/shell change wiring, configuration capture, and shutdown. |

A large per-language interface containing `Hover`, `Definition`, and `References`
would duplicate the standardized client and make every new operation a change to
every adapter. Use a small setup interface instead. The proposed shape is:

```go
type Adapter interface {
    Resolve(ctx context.Context, req ResolveRequest) (LaunchSpec, error)
}

type ResolveRequest struct {
    SessionDir string
    Path       string // File or query scope; empty means configured session scope.
    Server     ServerConfig
}

type LaunchSpec struct {
    Root                  string
    Dir                   string
    Command               []string
    Env                   []string
    InitializationOptions json.RawMessage
    Settings              json.RawMessage
}

type Dependencies struct {
    Adapters map[string]Adapter
}

func New(config Config, deps Dependencies) (*Manager, error)
```

These declarations are implemented in the public `lsp` package.
`ServerConfig` is serializable data with an ID, adapter name, selectors, and
launch/configuration overrides. Built-in presets expand into this same data.
`Dependencies` contains executable collaborators and is not serialized. Expose
it through harness dependencies for embedding applications that need custom
adapters. Copy the registry when constructing the manager; reject duplicate or
unknown adapter IDs instead of relying on package-global registration.

The manager first matches a file to a configured server and language ID, then
uses that server's adapter to resolve its launch specification. Resolution may
read project files but must not start processes, install tools, or modify files.
Adapters must support concurrent resolution or synchronize internally. Validate
and copy returned specifications before using them as instance/cache identities.
The manager owns process deduplication, restarts, and capability negotiation.

For workspace queries, enumerate only configured or already discovered roots;
an empty query path must not trigger unrestricted repository discovery. The
generic adapter accepts explicit roots or ordered root markers. The Go adapter
uses applicable `go.work` membership and `go.mod` boundaries. Go's
[workspace documentation](https://go.dev/gopls/workspace) explains why build scope
needs more care than finding the nearest marker. Explicit configuration wins;
ambiguous routing produces an actionable error.

Most new languages use the generic adapter and add no Go code. The first custom
adapter is `gopls`. If another server needs a special callback or protocol
extension, add a narrow, tested hook for that concrete need. Avoid giving every
adapter unrestricted access to JSON-RPC or introducing hypothetical hook methods.

**Proposed layout**

```text
lsp/
    config.go              # Serializable settings, presets, validation
    adapter.go             # Adapter, ResolveRequest, LaunchSpec
    adapter_generic.go     # Configuration-driven setup and root selection
    adapter_gopls.go        # Go workspace resolution
    manager.go             # Routing, shared instances, admission and Close
    client.go              # Common LSP methods and server callbacks
    process.go             # Persistent stdio process ownership
    documents.go           # Hashes, versions, open/change/save/close
    workspace.go           # Changes, reconciliation, watcher coverage
    positions.go           # URI/range and position-encoding conversion
    queries.go             # Normalized semantic operations
    references.go          # Bounded location handles
    pages.go               # Immutable result pages and cursors
    diagnostics.go         # Push/pull state, freshness, bounded waiting
    types.go               # Public query/result types
    errors.go              # Stable service error kinds
    internal/protocol/     # Private wire DTOs if the chosen library needs them
    testdata/              # Small workspaces and server fixtures
tool/
    lsp.go                 # Seven tools and strict argument DTOs
harness/
    lsp.go                 # Assembly, observation, mutation notifications
```

File boundaries are organizational guidance; create each as its behavior lands.
Keep built-in adapters in the `lsp` package initially to avoid an import cycle
between the manager and adapter subpackages using its exported types. Do not
make `lsp` import `tool`, `harness`, `agent`, or a model provider.

**Ordered milestones**

1. **Define configuration and prove server selection.** Add the package,
   serializable configuration, shared query/result vocabulary, setup interface,
   generic adapter, and Go preset. Resolve explicit roots and Go workspace
   membership. Distinguish server configuration IDs from language IDs: a server
   can support several languages, and a language can have several possible
   servers. Choose one primary semantic server per file using explicit
   configuration; report ties. Construction validates settings without launching
   anything. Verify nested modules, worktrees, root conflicts, and configuration
   copying using temporary fixtures.

2. **Establish one reliable persistent connection.** Select and pin the JSON-RPC
   dependency using the tradeoffs in the design. Preserve Strap's declared Go
   baseline. Implement process ownership, initialization, callbacks,
   configuration, capability tracking, deadlines, cancellation, and graceful
   close. `lsp_status` becomes the first complete service/tool slice. A missing
   binary is a query-time availability error, not a session startup failure.
   Verify concurrent first use starts one process, callbacks cannot deadlock
   an outstanding request, cancellation leaves other callers usable, and close
   reaps descendants. Use a fake stdio server for failure paths and a pinned
   `gopls` version for interoperability.

3. **Make the server view follow workspace changes.** Add document versions,
   hashes, position conversion, synchronization, file observation, and bounded
   reconciliation. Send changes using the negotiated synchronization mode.
   Reconcile cross-file and build/config changes before semantic requests; mark
   coverage uncertain after overflow or an incomplete scan. Implement change
   entry points before connecting them to the harness. Verify Unicode, CRLF,
   create/delete/rename, changed imports, external edits, and changes during
   queries. A stale query must retry once or report staleness explicitly.

4. **Add navigation with reusable, bounded results.** Implement `lsp_symbols`,
   `lsp_outline`, `lsp_inspect`, `lsp_navigate`, and `lsp_references`. Normalize
   protocol unions, preserve location versus declaration ranges, attach bounded
   source excerpts, and implement reference handles and immutable pages. Handle
   supported methods based on negotiated capabilities. Use complete typed
   reference/symbol forms, the existing `input` envelope, and explicit nulls.
   Verify schema/decoder agreement, overloaded names, external definitions,
   expired/stale handles, result truncation, and cursor continuation. Handles
   for dependency locations keep their originating server so navigation does
   not start unrelated servers in module caches.

5. **Add diagnostics with truthful freshness.** Implement the seventh tool,
   `lsp_diagnostics`, using pushed sets and capability-gated document pulls.
   Track server/URI provenance and versions; empty updates clear earlier sets.
   Bound waiting, distinguish unknown/pending from a completed empty report,
   and describe the coverage of cached workspace results. Add a coalesced
   refresh queue for changes. Verify delayed/out-of-order updates, versionless
   reports, server restart, dependent-file errors, explicit clears, and timeout.
   Multiple sources retain their identities when their diagnostic sets differ.

6. **Integrate ownership, tools, and the editing loop.** Add `Config.LSP`,
   dependency injection, configuration cloning, effective configuration capture,
   and owned-resource registration in the harness. Give the semantic read tools
   to all four roles. Wire successful file mutations and all shell completions
   into change observation, including the separately constructed research shell.
   Add CLI/eval configuration and basic TUI presentation. Deliver a capped
   diagnostic summary after edits at a suitable agent boundary, with detailed
   reads available through the tool. Verify identical behavior for TUI, HTTP,
   eval, and Go callers sharing harness assembly, and verify interrupted and
   partially failed startup cleanup. Historical results must remain readable
   without a live server.

7. **Qualify Go and demonstrate a second language.** Run the actual tool schemas
   against Strap's serving backend and representative models. Measure task
   success, reference recall/precision, invalid calls, tokens, cold/warm latency,
   and process memory. Use known-answer traversal and edit-feedback fixtures,
   plus tasks better served by text search. Configure
   [typescript-language-server](https://github.com/typescript-language-server/typescript-language-server)
   with `--stdio` and an explicit fixture root to exercise the generic adapter;
   pin the server and TypeScript versions in the integration job. Demonstrate
   definition, references, hover, and post-change diagnostics through the same
   tools. The advertised first-release preset remains Go; this second-server
   test establishes the extension boundary without promising broad TS support.

Milestones depend on the preceding ones. Tool and harness tests use the same
service contracts as they are introduced; milestone 6 integrates the full path.
An experimental Go feature is reviewable after milestone 6. Qualification in
milestone 7 is required before presenting the interface as stable.

**Integration details that affect implementation**

| Existing location | Planned change |
| --- | --- |
| [harness/session.go](../../harness/session.go) | Construct one manager, share read adapters, wire both normal and research shells, and keep role assembly explicit. |
| [harness/inspection.go](../../harness/inspection.go) | Copy and record effective LSP configuration and server identity; retain hashes/identifiers rather than raw environment secrets. |
| [harness/lifecycle.go](../../harness/lifecycle.go) | Close the manager through existing resource ownership after agent calls have joined; preserve cleanup retry behavior. |
| [tool/files.go](../../tool/files.go) | Add a lightweight post-mutation notification without awaiting analysis under the file lock. |
| [tool/shell.go](../../tool/shell.go) | Notify workspace uncertainty after every started shell call completes, including failure/cancellation that may have modified files. |
| [cmd/strap/options.go](../../cmd/strap/options.go) | Add `-lsp` and `-lsp-config`, independent of model catalog selection. |
| [cmd/strap-eval](../../cmd/strap-eval) | Apply the same LSP config loading to relevant runs; record versions and cold/warm setup in evaluation output. |
| [harness/prompts.go](../../harness/prompts.go) | Explain when semantic queries help, how to reuse references, how to interpret freshness, and when to use text search. |
| [internal/tui/tool_output.go](../../internal/tui/tool_output.go) | Render semantic locations/excerpts and distinguish pending or partial diagnostics; preserve raw-result fallback. |

Use ordinary tool result recording for semantic queries and bounded existing
session diagnostic events for process/load failures. Language diagnostics are a
separate concept from the existing workflow diagnostic-execution evidence.
Adding LSP does not create new work-ledger states or imply a completed validation.

File-change notification must not turn a committed edit into a failed edit
receipt if LSP refresh fails. Record that as a language-service failure and leave
the successful filesystem result intact. Hooks mark state dirty without waiting;
query synchronization is responsible for reconciling it. Shell changes cannot be
enumerated reliably from the command string, so request a bounded reconciliation
instead of attempting to parse shell commands.

Keep the enabled tool schemas stable even when individual servers are missing or
capabilities differ. Nil `Config.LSP` disables all LSP tools. The harness default
and CLI enable the Go, Rust, Python, JavaScript/TypeScript and Bash presets.
`-lsp-config <path>` replaces those presets with the supplied complete
configuration. Explicit `-lsp=false` wins. JSON unknown fields are rejected outside server-specific
initialization/settings payloads. Do not automatically load executable
configuration from repository files or install language-server binaries.

**How adding a language should work**

1. Add a server configuration or built-in preset: command/argv, file-to-language
   mapping, roots or markers, environment, and settings.
2. Use the generic adapter when that information is sufficient. Implement
   `Adapter.Resolve` only for workspace/setup rules requiring code.
3. Add a small real-server fixture covering the existing tools and unsupported
   capabilities. No new model tool, prompt family, or semantic client is needed.

Keep capabilities runtime-derived. A preset identifies a server and its setup;
it must not claim every version supports every operation. A server that lacks
references can still provide hover, outline, or diagnostics. Configuration-only
support is available immediately after integration, while advertised presets
require tested versions and documented limitations.

**Follow-on milestones**

| Milestone | Dependency and release condition |
| --- | --- |
| Call hierarchy | After navigation is qualified, add `lsp_calls` with incoming/outgoing directions, internal preparation, and retained opaque server data. |
| Additional supported presets | Promote TypeScript and add Python after full workspace/routing/diagnostic fixtures pass; reuse the shared tools. |
| Rename | Extend shared file mutation coordination, then add `lsp_rename` preview and `lsp_apply_edit` with retained patches and stale-base checks. Root/implementor only. |
| Code actions | List/resolve edit-producing actions through the same preview/apply path; support command-backed actions only with explicit tested handling. |

Do not delay read-only navigation for semantic mutation machinery. Conversely,
do not advertise reliable diagnostics before change observation and freshness
handling are complete.

**Completion checks for the first release**

- All seven read tools work end to end with `gopls`; missing/unsupported servers
  yield useful errors and ordinary file/shell work remains usable.
- Multiple agents share one process per resolved configuration/workspace without
  sharing state across separate worktrees or sessions.
- Direct writes, shell changes, and external edits invalidate relevant state;
  unresolved observation gaps are visible in result coverage.
- Unicode positions, reusable references, immutable pagination, and bounds are
  verified; no stale result is silently rebound to a different symbol.
- Diagnostic emptiness, freshness, and project coverage remain distinguishable.
- Cancellation, malformed traffic, process crash, and shutdown are tested without
  process leaks or indefinite waits. The relevant concurrent tests pass with
  Go's race detector.
- The same query/tool implementation passes a second real server fixture.
- Serving-backend schema qualification and task evaluations are recorded.
- README setup, configuration examples, supported server versions, and capability
  limitations match the shipped behavior.

The implementation uses `github.com/sourcegraph/jsonrpc2` v0.2.0 with a bounded
LSP framing codec and private wire DTOs, preserving Strap's Go 1.24 baseline.
Filesystem observation uses fsnotify; glob matching uses doublestar. The
shared-client and setup-adapter boundary above is unchanged.
