# Language servers (experimental)

Strap enables seven read-only language tools by default, backed by one shared
LSP client. Presets cover Go, Rust, Python, JavaScript, TypeScript, and Bash.
Install the servers for the languages you use on `PATH`:

| Language | Server command | File suffixes |
| --- | --- | --- |
| Go | `gopls` | `.go` |
| Rust | `rust-analyzer` | `.rs` |
| Python | `pyright-langserver --stdio` | `.py`, `.pyi`, `.pyw` |
| JavaScript / TypeScript | `typescript-language-server --stdio` | `.js`, `.mjs`, `.cjs`, `.jsx`, `.ts`, `.mts`, `.cts`, `.tsx` |
| Bash | `bash-language-server start` | `.sh`, `.bash`, `.bashrc`, `.bash_profile`, `.bash_login`, `.bash_logout` |

These commands install the tested server versions (run only those you need):

```sh
go install golang.org/x/tools/gopls@v0.20.0
npm install --global typescript-language-server@5.3.0 typescript@5.9.3
npm install --global pyright@1.1.414 bash-language-server@5.7.1
rustup component add rust-analyzer rust-src
go run ./cmd/strap -C /path/to/project
```

The same flags work with `strap-eval run` and `strap-eval interaction run`.
Servers start on the first semantic query; `lsp_status` does not launch anything.
A missing executable fails that query without disabling file or shell tools.
`-lsp=false` disables the feature and overrides a supplied config file.
Servers run locally with host permissions and inherited environment plus explicit
overrides. Strap neither installs servers nor loads executable configuration from
project files automatically.

Language selection uses the longest matching configured file suffix, then
workspace roots. JavaScript and TypeScript share a server instance for the same
root. Extensionless scripts and shebang-based detection are not included.
JSX and TSX use the corresponding React language IDs.

[rust-analyzer](https://rust-analyzer.github.io/book/installation.html) uses the
project's Rust toolchain and needs `rust-src` for standard-library analysis.
[Pyright](https://github.com/microsoft/pyright) needs the project's Python
environment for accurate imports; use its project configuration or the server's
`python.pythonPath` setting for a specific interpreter.
[typescript-language-server](https://github.com/typescript-language-server/typescript-language-server)
uses TypeScript for both JavaScript and TypeScript. JavaScript type diagnostics
depend on `checkJs` or `// @ts-check`.
[bash-language-server](https://github.com/bash-lsp/bash-language-server) provides
navigation directly; install `shellcheck` separately for lint diagnostics.
Its hover documentation is useful at a function usage rather than its declaration.

## Tools

| Tool | Purpose |
| --- | --- |
| `lsp_status` | Configured/running servers, roots, reported versions, available operations and startup errors. |
| `lsp_symbols` | Search workspace symbols by name, optionally within a path. |
| `lsp_outline` | File symbols, with hierarchy depth 1–8. |
| `lsp_inspect` | Hover documentation and optional bounded source. |
| `lsp_navigate` | Definition, declaration, type definition, or implementation. |
| `lsp_references` | References, optionally including the declaration. |
| `lsp_diagnostics` | Bounded checks of explicit files, or cached publications. |

All roles receive the same tools and instructions to use symbols/outline for
discovery, inspect for documentation, and navigation/references for traversal.
They are advised to reuse handles, retry indexing results, request diagnostics
after edits, and use text search for literals or unavailable servers.
Each tool takes the normal `input` object;
every declared field is required, with `null` selecting an optional default.
For example:

```json
{"input":{"path":"main.go","depth":1,"limit":50,"cursor":null}}
```

Use a returned reference with `lsp_inspect`:

```json
{"input":{"target_kind":"reference","ref":"loc_RETURNED_VALUE","include_source":true}}
```

Or identify a symbol on a source line you have read:

```json
{"input":{"target_kind":"symbol","path":"main.go","line":10,"symbol":"Run","context":null,"include_source":true}}
```

The model supplies an exact unqualified identifier, not a character column.
Strap finds that identifier on the **1-based** line and calculates the position
against the synchronized source. If it occurs twice on the line, supply a unique
verbatim `context` fragment containing just the intended occurrence, such as
`Run("preview")`. Missing or ambiguous matches fail without guessing a nearby
line or selecting the first match. Copy paths exactly, including spaces.
The matcher recognizes identifier boundaries; it is not a parser and does not
distinguish code from comments or strings. Context can exclude those occurrences.
Operators and arbitrary expressions require a returned ref or the library API.

Returned columns count Unicode code points from **1**; tabs count as one. The
client converts to the server's negotiated UTF-8/16/32 units. Range ends are
exclusive. The Go library retains `Target{Path, Line, Column, ExpectedText}` for
programmatic callers; model tools expose only reference and symbol targets.
A reference identifies the
observed file contents and server generation; edits, restarts, and eviction
require rediscovery. It is not a persistent symbol ID. Non-file locations retain
their URI but cannot be opened using a Strap reference.

Pages default to 50 items (maximum 200), with a 16 KiB output budget. To continue,
repeat the original arguments and replace `cursor` with `next_cursor`. Retained
pages expire after five minutes and may be evicted earlier. Continuations retain
the captured result; changed files mark it stale. `truncated` means some results
were not retained, so narrow the query instead of expecting another cursor.

## Configuring another language

Supply the complete server list with `-lsp-config /path/to/lsp.json`; it replaces
the built-in presets. Paths in
`roots` are relative to the session working directory. Commands are argument
arrays executed directly, without shell interpolation:

```json
{
  "servers": [
    {
      "id": "go",
      "adapter": "gopls",
      "command": ["gopls"],
      "languages": {".go": "go"}
    },
    {
      "id": "typescript",
      "command": ["typescript-language-server", "--stdio"],
      "languages": {".ts": "typescript", ".tsx": "typescriptreact"},
      "root_markers": ["tsconfig.json", "package.json"]
    }
  ]
}
```

Optional server fields are `roots`, `root_markers`, `fallback_to_session`, `env`,
`initialization_options`, and `settings`. Unknown host fields are rejected;
server-owned initialization/settings JSON stays opaque. Use settings keyed by
the server's requested configuration section where applicable, such as
`{"gopls":{"staticcheck":true}}`. Client configuration callbacks resolve dotted
sections. A server's capabilities determine which operations actually work.

The generic adapter selects the longest containing explicit root, otherwise the
nearest configured marker up to the session directory. If no markers are
configured, it uses the session directory; with markers, a missing marker is an
error unless `fallback_to_session` is true. If multiple configurations match a file, narrow their roots or file
mappings; routing does not silently choose one. Workspace symbol searches use
configured/discovered roots, without crawling for arbitrary projects.
Scopes without matching source files do not start unrelated servers.

Rust selects the nearest `Cargo.toml` or `rust-project.json`; set explicit `roots`
to share one process across Cargo workspace members. Python uses
`pyrightconfig.json`, `pyproject.toml`, `setup.py`, `setup.cfg`, or
`requirements.txt`. JavaScript/TypeScript uses `tsconfig.json`, `jsconfig.json`,
or `package.json`. Bash uses `.git`. Python, JavaScript/TypeScript and Bash fall
back to the session directory for standalone files.

The Go adapter resolves `go.mod` and membership in the nearest `go.work`, and
pins automatic `GOWORK` selection to the resolved root. Explicit `GOWORK=off` or
an absolute workspace path is supported. A module excluded by an automatically
discovered parent workspace runs separately. Go results still depend on build
tags, GOOS/GOARCH, and the server-selected build.

## Library integration

```go
cfg := harness.DefaultConfig()
cfg.Dir = projectDir
session, err := harness.New(ctx, cfg, harness.Dependencies{})
```

`harness.DefaultConfig()` includes `lsp.DefaultConfig()` with all five servers.
Set `cfg.LSP = nil` to disable the tools. To select only one preset, assign the
address of `lsp.GoConfig()`, `lsp.RustConfig()`, `lsp.PythonConfig()`,
`lsp.TypeScriptConfig()`, or `lsp.BashConfig()` to `cfg.LSP`.

A session owns one `lsp.Manager`; actors share it, independent sessions/worktrees
do not. Configuration is copied at construction. Effective configuration records
server IDs and a configuration fingerprint, excluding raw environment values,
command arguments and arbitrary settings. Normal tool results retain locations
and server/root/generation provenance for replay.

Most languages require only configuration. For special workspace discovery,
implement `lsp.Adapter.Resolve(context.Context, lsp.ResolveRequest)
(lsp.LaunchSpec, error)` and supply it through
`harness.Dependencies{LSP: lsp.Dependencies{Adapters: ...}}`. The adapter reads setup
information; the common client owns all semantic methods and processes.
Embedding applications may also use `lsp.New`, its typed query methods, and
`tool.LSPTools` directly; close the manager after joining callers.

## Freshness and limits

Successful file edits notify the manager after releasing the file lock. All
started shell calls notify on completion, including failure/cancellation.
Filesystem watchers provide external-change hints. Queries rescan bounded
workspace contents and synchronize open documents before requesting results.
Server watcher registrations filter filesystem notifications by glob and event
kind. Callbacks cannot apply edits or execute commands.

Diagnostic updates replace the same producer's previous set for that file; an
empty set clears it. Push and pull sets are retained separately because servers
such as rust-analyzer use independent compiler and analyzer producers.
Requested files use document pulls when supported, otherwise wait for
pushes for up to 1.5 seconds by default. Each checked file reports freshness.
`unknown` includes unversioned or missing reports; `stale` identifies known
snapshot mismatches. Even `synchronized` means a matching observed document
version, not proof of a correct whole project. Cached results never establish
workspace completeness. Always run appropriate tests/builds.

Initialization does not imply completed indexing. During the first five seconds,
empty semantic reads are retried within the request deadline. Standard work-done
progress and diagnostic refresh requests are observed; active indexing is marked
partial. Diagnostic publication is asynchronous, so a later query can add or clear
messages. Retry partial or pending results before concluding that code is absent
or analysis is complete.

Change hints coalesce in the background and refresh **already opened files on
running servers**. They do not launch every configured server or discover new
files for analysis. A later tool boundary can append up to five cached,
version-matched diagnostics, deduplicated per actor. Explicit `lsp_diagnostics`
is the dependable way to request feedback for newly edited files. Summaries do
not distinguish pre-existing errors from errors introduced by an edit.

Defaults: eight server instances; 1 MiB per file; 8 MiB per wire message; 10,000
workspace entries and 32 MiB scanned per reconciliation; 128 open documents per
server; an 8 MiB result cache; up to 4,096 location references and 64 retained
queries. A query retains at most 2,000 items and 1 MiB before paging. Diagnostic
caches are bounded separately by the same byte limit per server. Timeouts and
resource bounds are host-configurable through `lsp.Config` JSON fields.

Scans exclude `.git`, `node_modules`, `.venv`, `venv`, `vendor`, `target`,
`__pycache__`, and `.cache`, and skip symlink entries. Unreadable/oversized files, scan limits, and
lost watch events report partial coverage. Dependencies outside the root are not
watched; direct result/reference content is checked when read. Reconciliation
hashes files and serializes queries across this first-version manager, so large
workspaces and concurrent actors may see additional latency. The API does not
claim project-wide convergence or atomic snapshots against external writers.

macOS and Linux process groups provide shutdown and descendant cleanup. A
cancelled request is sent `$/cancelRequest`; if the peer does not answer within a
short grace period, it is stopped. A later query can restart it, with at most two
failures per instance before requiring a corrected session. Existing references
become stale after restart. Servers that deliberately leave their process group
are outside this cleanup mechanism.

## Verification and release status

Run offline contracts, lifecycle, synchronization and cancellation checks with
`go test -race ./lsp ./tool ./harness ./internal/lspconfig`. Tests use a real stdio
subprocess fixture for failure paths. The opt-in interoperability fixture exercises
symbols, outline, hover, definition, references, stale handles, and diagnostic
appearance/clearing against all six languages. Install the servers above plus
Cargo, Python and ShellCheck before running:

```sh
STRAP_LSP_REAL=1 go test -race ./lsp -run TestRealLanguageServers -count=1 -v
```

Verified locally on September 20, 2026 with gopls **v0.20.0**, TypeScript language
server **5.3.0**, TypeScript **5.9.3**, rust-analyzer **1.95.0**, Pyright
**1.1.414**, bash-language-server **5.7.1**, and ShellCheck **0.11.0**.
CI pins the language servers and uses its distribution's ShellCheck package.
Unversioned publications remain `unknown` even when their messages arrive and
clear as expected. The opt-in test fails if a required executable is missing.

Schema qualification can be rerun against a tool-capable vLLM endpoint. It checks
tool selection, strict argument validity, and the requested argument values:

```sh
STRAP_LIVE_LSP=1 STRAP_LIVE_BASE_URL=http://127.0.0.1:8000 \
  STRAP_LIVE_MODEL=your-model \
  go test ./tool -run TestLiveLanguageSchemas -count=1 -v
```

On September 20, the real Qwen3.6 deployment passed **24/24** schema cases after
clarifying opaque-handle usage. A controlled comparison found that original
descriptions also worked when a preceding tool receipt established the handle.
Two subsequent trials through the normal harness and real gopls recovered the
required fixture facts and reused returned refs, but both needed recovery from
incorrect manually chosen positions. Manual review also found inaccurate extra
claims. The strict zero-rejection workflow checks therefore failed; full answer
correctness is not established by the required-fact checks.
See [the original measurements](evals/2026-09-20-lsp-qwen36.md).

The [reliability follow-up](evals/2026-09-20-lsp-reliability.md) replaces model
column arithmetic with source identifiers and tests first-call correctness with
actual gopls results. It separates tool misuse, evaluator mistakes, rejected
calls, required facts, and manual answer review. The live edge suite covers tabs,
Unicode, repeated identifiers, shadowing, duplicate declarations, filenames with
spaces, pagination, diagnostic freshness and stale-handle rediscovery:

```sh
STRAP_LIVE_LSP_EDGES=1 STRAP_LIVE_BASE_URL=http://127.0.0.1:8000 \
  STRAP_LIVE_MODEL=your-model \
  go test ./tool -run TestLiveLanguageEdges -count=1 -v
```

This is a small qualification sample, not a general task-success benchmark.
The feature stays experimental while broader traversal and model reliability
evaluation remain open. Call hierarchy, rename, and code actions are follow-on
work; this release adds semantic reads only.
