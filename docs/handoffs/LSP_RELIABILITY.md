# Handoff: continue LSP reliability work

Prepared September 20, 2026. Repository: `github.com/stevemurr/strap`.

## Objective and first task

Continue implementing and evaluating LSP reliability improvements. The user's
priority is to eliminate avoidable tool misuse and the extra model turns spent
recovering. A correct eventual answer does not excuse a wrong first call.
Preserve necessary discovery and freshness checks; do not optimize away checks
that prevent silently querying the wrong code.

Start with the remaining path-copy failure. Qwen3.6 sometimes changes
`preview/emoji file.go` into `preview/emoji/file.go`, even after an exact-copy
instruction. Reproduce it using actual file-read receipts and the normal harness,
then compare a bounded interface improvement against the current baseline.
Prefer host-enforced correctness over accumulating more prompt instructions.

Read these before editing:

1. [Latest findings and measurements](../evals/2026-09-20-lsp-reliability.md).
2. [Current tool contracts and setup](../lsp.md).
3. [Original model evaluation](../evals/2026-09-20-lsp-qwen36.md), for historical
   context only; it used the previous numeric-position interface.

## Workspace state: preserve existing work

The working directory is `/Users/murr/Code/github.com/stevemurr/strap`. At handoff,
the branch is `main`, HEAD is `fd64550`, and the LSP implementation and evaluations
are **uncommitted**. Many required files, including `lsp/`, `tool/lsp*.go`, the live
tests and documentation, are untracked. A fresh checkout of HEAD does not contain
this work. If moving to another workspace, transfer the working tree additions
and required artifacts; a tracked-file diff alone is insufficient.

There are unrelated user TUI changes mixed into the working tree. Preserve
`internal/tui/activity_view.go`, `agent_identity.go`, `plan.go`, `plan_test.go`,
`progress.go`, `streams_test.go`, `tool_layout.go`, `tool_output.go`, `tui.go`,
`presentation_test.go`, and `syntax.go`. Some files contain both LSP and user
changes. `go.mod` also includes the user's Chroma dependency change. Inspect
diffs before modifying shared files; do not reset, clean, overwrite or broadly
stage the working tree. No commit or publication was requested.

The user previously authorized implementation and real-model testing at the
deployment below. Only synthetic fixture code has been sent; do not change the
model server configuration as part of this work. No evaluation process was left
running at handoff. Recheck repository instructions and current state on arrival.

## What is implemented

LSP is enabled by default in CLI, harness and eval sessions; `-lsp=false` disables
it. Go, Rust, Python, JavaScript, TypeScript and Bash are configured through five
server presets. Language routing uses configured file suffixes and workspace
roots; extensionless/shebang detection is not implemented. Servers start lazily,
and missing executables do not prevent session startup.

A shared public `lsp.Manager` owns protocol behavior, lifecycle, synchronization,
references, paging and diagnostics. Language adapters implement only setup/root
resolution through `Adapter.Resolve`; they do not each reimplement semantic
methods. All agent roles receive seven read-only tools:
`lsp_status`, `lsp_symbols`, `lsp_outline`, `lsp_inspect`, `lsp_navigate`,
`lsp_references`, and `lsp_diagnostics`. Rename, code actions and call hierarchy
remain outside the current feature.

The latest change removes character columns from **model-facing** targets.
Inspect, navigate and references accept either an issued handle or an exact
identifier on an observed source line. For example, `lsp_navigate` accepts:

```json
{"input":{"target_kind":"symbol","path":"invoice/total.go","line":6,"symbol":"RateFor","context":null,"relation":"definition","limit":null,"cursor":null}}
```

Its preferred handle form is:

```json
{"input":{"target_kind":"reference","ref":"loc_ACTUALLY_RETURNED_HANDLE","relation":"definition","limit":null,"cursor":null}}
```

All declared fields are required; nullable fields must be present, using `null`
for defaults. `context` is a unique verbatim single-line fragment containing just
the intended occurrence when an identifier repeats, e.g. `RateFor("retail")`.
The host calculates code-point columns and converts to negotiated UTF-8/16/32.
Missing or ambiguous matches fail. The Go library still accepts guarded numeric
positions for programmatic callers; the model schema rejects them.

Preserve these invariants:

- Issued references bind to observed content and server generation. Edits,
  restarts and eviction require rediscovery; never silently rebind them.
- Never choose the first duplicate, search a nearby line or fuzzily repair paths.
- The anchor matcher is lexical: strings/comments participate in ambiguity;
  operators and arbitrary expressions are outside the symbol-target form.
- Anchors are not snapshot-bound. A same-name occurrence at the same line after
  an external edit can still match. This remains a reliability edge.
- Paging preserves captured results and requires unchanged query arguments.
  `truncated`, partial, stale and indexing states carry meaning.
- Empty cached/unknown diagnostics do not establish a clean file or project.
  Explicit-file checks still depend on server/build scope and freshness.

## Code map

| File | Responsibility |
| --- | --- |
| [tool/lsp.go](../../tool/lsp.go) | Seven definitions, descriptions, strict target branches and service dispatch. |
| [harness/prompts.go](../../harness/prompts.go) | `languageInstruction`: grounding, handle reuse, targeting, operation selection and freshness guidance. |
| [lsp/targets.go](../../lsp/targets.go) | Target validation and unique identifier/context resolution. |
| [lsp/targets_test.go](../../lsp/targets_test.go) | 24 resolver cases, encoding round trips and mixed-target rejection. |
| [lsp/workspace.go](../../lsp/workspace.go) | Synchronization and `Manager.target`, including reference freshness checks. |
| [lsp/types.go](../../lsp/types.go) | Public target and normalized result types. |
| [lsp/queries.go](../../lsp/queries.go), [diagnostics.go](../../lsp/diagnostics.go), [readiness.go](../../lsp/readiness.go) | Semantic queries, diagnostics and indexing/readiness behavior. |
| [harness/lsp.go](../../harness/lsp.go), [session.go](../../harness/session.go) | Shared manager ownership, registration and diagnostic feedback. |
| [tool/files.go](../../tool/files.go) | Source reads and mutation hooks; inspect this if experimenting with source-derived targets. |
| [tool/lsp_live_edges_test.go](../../tool/lsp_live_edges_test.go) | First-call choices executed against real gopls; current suite has ten cases. |
| [harness/lsp_live_test.go](../../harness/lsp_live_test.go) | Normal harness, competing local/workflow tools, real gopls, saved metrics and event traces. |
| [tool/lsp_live_test.go](../../tool/lsp_live_test.go) | Eight schema/tool-selection cases. |
| [tool/lsp_test.go](../../tool/lsp_test.go) | Offline schema/decoder agreement and dispatch contracts. |
| [lsp/real_test.go](../../lsp/real_test.go) | Real-server interoperability across all six languages. |

Architecture context is in [LSP_DESIGN.md](../architecture/LSP_DESIGN.md) and
[LSP_IMPLEMENTATION_PLAN.md](../architecture/LSP_IMPLEMENTATION_PLAN.md).

## Evidence to retain

| Phase | Result | Interpretation |
| --- | --- | --- |
| Historical numeric-position workflows | All three needed position-error recovery. | Motivated removing column arithmetic from model inputs. |
| First three symbol-target workflows | Rejected calls: 0 / 2 / 0. | One invented both a project path and a handle. |
| Three workflows after grounding instructions | Zero rejected calls, invalid schemas or invented refs. | Small sequential sample, not a guarantee. |
| Two workflows with final descriptions | Zero rejected calls, invalid schemas or invented refs; 4 / 5 model calls. | Required facts present, but extra prose still included unsupported claims. |
| Initial nine edge cases, twice each | Corrected 14/18. | Two altered paths and two unnecessary inspections. |
| Focused four edge cases, twice each | 7/8. | Navigation 4/4; quoted paths 2/2; original unquoted path 1/2. Remaining failure retained. |

The latest five workflows used valid symbol targets without returned-ref reuse.
Reference reuse is covered separately by edge tests; do not infer it was
exercised by those workflows. The edge suite exposes only seven LSP tools and a
short test system prompt. It does **not** use the full harness instruction/tool
roster; normal-harness trials are a separate test layer. Real-model workflows so
far cover Go only. Interoperability tests cover all six languages without a model.

Two evaluator errors were corrected while preserving original artifacts:

- The local-shadow expected column was 19, but the correct location was 20.
  The current test derives it from source. Original raw edge score: 13/18;
  corrected score: 14/18.
- A workflow test unnecessarily demanded at least one returned-ref reuse.
  Valid symbol-only workflows now pass; unissued refs still fail.

Keep required-fact coverage separate from full answer correctness. Existing
checks are narrow, and manual review found wrong package counts, invented dollar
units, an incorrect source range and unsupported provenance/import claims.
Likewise, an allowed tool name and valid JSON do not prove the intended symbol
was selected. Assert actual returned locations and semantics.

Local raw data is under the ignored directory
`eval/results/2026-09-20-lsp-reliability/`. It will not appear in an ordinary clone.
`manual-review.json` records evaluator corrections and answer findings;
`final-source/` snapshots the final implementation, not every historical variant.
`strap-lsp-symbol-*.jsonl` logs distinguish phases, and `*.metrics.json` files
contain calls, errors, usage and answers. Edge filenames ending
`T011903.257719000` / `T012018.071555000` hold the original 18 calls;
`T012523.802709000` / `T012601.517739000` hold the focused eight. Original
position-based artifacts are in `eval/results/2026-09-20-lsp-qwen36/`.

## Next work, in priority order

1. **Path and handle grounding.** Test unquoted user prose, quoted user prose and
   actual `read_file` receipts as separate contexts. Include spaces, Unicode,
   duplicate basenames, and a fixture where both the correct and mistakenly
   rewritten path exist. A wrong-but-valid target is more serious than a rejected
   one. Compare direct edge tests with full-harness behavior.
2. **Evaluate one structural improvement.** Candidates include copyable semantic
   targets in source-read receipts or bounded selections among actually issued
   targets. These are proposals, not implemented decisions. Account for local
   variables, source-read cost, schema/prompt size, reference lifetime, actor
   sharing and cache eviction. Do not replace one guessed string with another
   or automatically reinterpret an incorrect call as a different operation.
3. **Exercise ambiguity and change.** Add comment/string duplicates, identical
   calls on one line, shadowing, stale source lines, same-name replacement at an
   unchanged line, edits between discovery and execution, server restart and
   reference eviction. Validate both safe rejection and correct selection.
4. **Broaden result-state and language coverage.** Model tests should include
   missing servers, unsupported operations, delayed indexing, stale/partial
   pages, diagnostic clearing and unknown freshness. Then extend real-model
   traversal to Rust, Python, JS/TS and Bash, including language-specific names,
   imports, environments and build scope.

For the next bounded iteration, complete priorities 1–2 with regression coverage
and a measured comparison before attempting the entire remaining matrix.
Choose trial counts and success criteria before running. Preserve every failure,
record prompt/schema/sampling changes, and do not rerun selectively until green.
Separate transport failure, schema failure, wrong operation, wrong target,
necessary freshness recovery, avoidable recovery, required facts and answer
fidelity. Record model calls, rejected calls, tokens and elapsed time.

## Running the checks

The user-provided deployment is
`https://spark-0368.tail11899.ts.net:8443`, model `qwen3.6`. It previously advertised
`unsloth/Qwen3.6-35B-A3B-NVFP4-Fast`. Verify `/v1/models` and endpoint reachability
when resuming; a deployment change is a new evaluation configuration.

From the repository root, use a new output directory for each experiment:

```sh
export STRAP_LIVE_BASE_URL=https://spark-0368.tail11899.ts.net:8443
export STRAP_LIVE_MODEL=qwen3.6
export STRAP_LIVE_PROFILE=qwen3.6
export STRAP_LIVE_LSP_OUTPUT="$PWD/eval/results/lsp-next-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$STRAP_LIVE_LSP_OUTPUT"

# Ten current edge cases, twice each; requires gopls.
STRAP_LIVE_LSP_EDGES=1 go test ./tool \
  -run '^TestLiveLanguageEdges$' -count=2 -timeout=15m -json \
  > "$STRAP_LIVE_LSP_OUTPUT/edges.log.jsonl" 2>&1

# Normal harness, real gopls and competing tools; three complete trials.
STRAP_LIVE_LSP=1 STRAP_LIVE_LSP_TRIALS=3 go test ./harness \
  -run '^TestLiveLanguageTraversal$' -count=1 -timeout=15m -json \
  > "$STRAP_LIVE_LSP_OUTPUT/traversal.log.jsonl" 2>&1

# Narrow schema/intent smoke test, not a workflow-success measurement.
STRAP_LIVE_LSP=1 go test ./tool \
  -run '^TestLiveLanguageSchemas$' -count=1 -v
```

These opt-in tests skip without their environment flags. The harness uses the
bundled Qwen3.6 sampling profile: temperature 1, top-p 0.95, top-k 20, repetition
penalty 1.1, thinking enabled, capped at 8,192 output tokens and 24 calls per trial.
The edge probe sets similar sampling directly. The schema probe uses temperature
0 and a smaller token cap; do not pool its outcomes with workflow results.
The old `TestLiveLanguageDescriptions` A/B probe retains historical descriptions
but uses the current schema; it cannot exactly reproduce the original interface.

Previously passed validation:

```sh
go test ./...
go vet ./...
go build ./...
go test -race ./lsp ./tool ./harness ./internal/lspconfig
STRAP_LSP_REAL=1 go test -race ./lsp \
  -run '^TestRealLanguageServers$' -count=1 -timeout=5m -v
```

The final command needs all servers and associated toolchains. Tested versions:
gopls 0.20.0, rust-analyzer/Rust 1.95.0 with rust-src, Pyright 1.1.414,
typescript-language-server 5.3.0 with TypeScript 5.9.3, bash-language-server 5.7.1,
and ShellCheck 0.11.0. On this Mac, Pyright was installed in
`/tmp/strap-lsp-servers/node_modules/.bin`; include that directory on `PATH` if
it still exists. Rust binaries were under `$HOME/.cargo/bin`. Do not assume
temporary installations survive or change the user's default Rust toolchain.
See [CI setup](../../.github/workflows/ci.yml) and [installation notes](../lsp.md).
An earlier unrelated macOS `tool.TestShellTimeoutAndDescendantCleanup` failure
reported EPERM and passed on rerun; retain evidence if it recurs rather than
silently attributing it to LSP or suppressing it.

Finish the iteration with a concrete code change, meaningful regression checks,
a report of all preselected live trials and their remaining failures, and updated
tool documentation if the contract changed. Keep the feature experimental until
broader evidence supports a stronger reliability claim.
