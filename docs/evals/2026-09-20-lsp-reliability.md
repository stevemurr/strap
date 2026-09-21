# LSP reliability follow-up, September 20, 2026

Recovery is a cost, not a passing first call. This follow-up changes the source
target interface after the [initial Qwen3.6 evaluation](2026-09-20-lsp-qwen36.md)
found repeated column mistakes. It also measures errors that schema validation
cannot detect: choosing a neighboring tool, inventing a handle, changing a path,
or treating incomplete results as complete.

Tests use the user-supplied vLLM deployment, model `qwen3.6`
(`unsloth/Qwen3.6-35B-A3B-NVFP4-Fast`), and local gopls v0.20.0. Only synthetic
fixture code is sent. No serving settings were changed. Artifact timestamps are
UTC, on September 21; the runs were on September 20 in America/Los_Angeles.

## Interface changes

The three target-taking tools now accept either an issued `ref`, or
`target_kind="symbol"` with `path`, 1-based `line`, exact unqualified `symbol`,
and nullable `context`. The host finds the identifier on that synchronized source
line and calculates the column and LSP encoding. Numeric columns are removed
from the model schema; the Go library retains them for programmatic clients.

For `RateFor("vip") + RateFor("retail")`, the latter call uses
`symbol="RateFor"`, `context="RateFor(\"retail\")"`. Both the context and selected
identifier must be unique. Wrong lines, missing matches and ambiguous occurrences
fail explicitly. The host never searches nearby lines, selects the first match,
repairs an invented path or silently rebinds an expired reference.

The prompt requires paths, lines and handles observed in tool results or supplied
by the user. It discourages invented project directories and unread-file offsets.
Descriptions explicitly distinguish hover/source inspection from definition
navigation, and declaration-location filtering from finding callers. Reference
fields explain that handles must actually have been supplied. Source-path fields
say to preserve spaces and relative paths.

## Reliability boundaries

| Edge | Observed behavior | Current protection and remaining limit |
| --- | --- | --- |
| Column arithmetic, tabs, Unicode | All three earlier position-based workflows needed recovery. | Host calculates columns. Unit cases cover UTF-8/16/32 conversion. The model must still select the right file, line and identifier. |
| Repeated identifiers and local shadowing | Both repeated-call requests selected the intended second occurrence. One shadowed-variable request chose inspection instead of navigation. | Unique verbatim context selects an occurrence; clearer operation descriptions improved direct navigation in the focused rerun. |
| Same name in different packages | Both first-call reference tests reused the correct issued handle; workflows distinguished the unrelated declaration. | References preserve semantic identity. A bare workspace name is discovery, not enough to choose among duplicates. |
| Invented paths and handles | One initial symbol-target workflow invented `/project/invoice/total.go`, offset 34, and an unissued `loc_` handle. | Stronger grounding instructions; existing path and handle validation reject these. Instructions do not make invention impossible. |
| Spaces in filenames | Initial requests changed `preview/emoji file.go` into `preview/emoji/file.go` twice. This still happened once after the description change. | Exact copying instruction helps; backtick-delimited paths passed 2/2. Unquoted prose remains format-sensitive. No fuzzy path repair is performed. |
| Inspection versus navigation | Two initial edge calls inspected the usage when the task asked for its definition. | Descriptions now say to navigate directly. The four corresponding focused requests all selected navigation. This is a small sample, not a guarantee. |
| Pagination | Both initial cases continued the actual cursor and executed successfully. | Cursor validation requires unchanged arguments. Captured pages can become stale; this case does not qualify model handling of every partial/stale page state. |
| Diagnostic freshness | Both explicit-file checks found a real error despite an earlier empty cache. | Unknown/cached diagnostics cannot establish a clean project. Pending analysis, broad build coverage and server failure need broader model evaluation. |
| Stale handles | Both cases rediscovered the declaration after an edit moved it. | Old handles remain rejected. This refresh is necessary work, not an avoidable misuse; silently retargeting could produce a wrong answer. |
| Answer fidelity | Correct calls and required facts did not prevent inaccurate extra claims. | Manual review stays separate from tool correctness; the evaluator does not call its substring checks full task success. |

The identifier matcher is lexical, not a language parser. Quoted/commented names
can require context, and operators/arbitrary expressions are outside its target
form. Unlike a reference, a source anchor does not bind to previously observed
content: a different same-name occurrence at the same line can still match after
an external edit. Prefer references when available. Eliminating path/handle
invention structurally would require constraining targets to issued selections
or attaching semantic targets to source reads; that is not implemented here.

## Measurements

The normal harness uses its complete local/workflow tool roster, a real shared
language manager and real gopls. The user request specifies the investigation,
not a tool sequence. Sampling uses the bundled Qwen3.6 profile: temperature 1,
top-p 0.95, top-k 20, repetition penalty 1.1, thinking enabled, with 8,192 maximum
output tokens, 24 model calls and four minutes per trial. All fixture files must
remain unchanged. Every rejected execution counts even if later recovered.

The first three symbol-target workflows used 5/5/5 model calls, taking
37.0/31.9/47.5 seconds. Rejections were 0/2/0, with zero schema-invalid calls.
The middle workflow invented both the path and reference described above.
The next three workflows, after grounding instructions, used 6/4/4 model calls,
taking 47.8/27.8/33.0 seconds, with zero rejected calls, schema-invalid calls or
invented references. Two final-description workflows then used 4/5 model calls,
taking 44.9/47.4 seconds, again with zero rejected calls, schema-invalid calls or
invented references. Required fixture facts were present in all eight answers.
The latest five workflows used valid symbol targets without reusing handles;
handle reuse remains separately exercised by the edge cases. These sequential
experiments are not a randomized latency comparison or a zero-error guarantee.

An eight-case schema probe on the symbol interface passed 8/8 before the final
navigation/path description edits. The initial edge suite ran nine cases twice,
using all seven LSP definitions and real server receipts/results. It enforces one
appropriate first tool and executes it, then checks the resulting source location
or diagnostic. After correcting an evaluator error, it passed **14/18**: the four
model failures were two altered paths and two unnecessary inspections. Other
cases covered tabs, Unicode, repeated identifiers, duplicate declarations,
pagination, explicit diagnostics and stale-handle rediscovery.

After navigation/path description changes, a predefined focused rerun covered
the three problem cases plus a backtick-delimited filename variant, twice each.
It passed **7/8**: navigation was correct in 4/4, quoted paths in 2/2, and the
original unquoted path in 1/2. That remaining failure is retained. No more retries
were run to replace it with a passing batch. This edge probe offers only LSP tools;
normal-harness workflows separately test competition with file and shell tools.

Two evaluator problems were corrected without rewriting original artifacts:

- One local-shadow case expected column 19; gopls correctly returned column 20.
  The expected column is now derived from the fixture's declaration. The original
  raw score was 13/18; manually correcting that false negative gives 14/18.
- The first workflow initially required at least one returned-ref reuse, despite
  accepting symbol targets. A valid symbol-only workflow is now allowed; invented
  references still fail, and specific reuse is checked in the edge suite. Its
  original failure and zero-rejection metrics are both retained.

Manual review of the initial symbol workflows found a four-packages claim for a
five-package fixture and unsupported dollar units. One grounded answer's stated
source range omitted the closing brace. The first workflow with final descriptions
invented the label “GOLDS SYNCHRONIZED” while summarizing valid LSP evidence.
The second added an unsupported claim that the legacy package was imported;
there is no such import in the fixture. These are answer-quality errors, distinct
from rejected tools and fixture facts.

## Reproduction and artifacts

Set the deployment and output directory, then run the opt-in probes:

```sh
export STRAP_LIVE_BASE_URL=https://spark-0368.tail11899.ts.net:8443
export STRAP_LIVE_MODEL=qwen3.6
export STRAP_LIVE_LSP_OUTPUT="$PWD/eval/results/2026-09-20-lsp-reliability"

STRAP_LIVE_LSP_EDGES=1 go test ./tool -run '^TestLiveLanguageEdges$' -count=2 -v
STRAP_LIVE_LSP=1 STRAP_LIVE_LSP_TRIALS=3 \
  go test ./harness -run '^TestLiveLanguageTraversal$' -count=1 -v
```

The current suite includes the quoted-path variant, so a full rerun has ten cases.
Each change in instructions constitutes a new configuration; runs above reproduce
the current implementation, not the earlier wording. Raw metrics and event traces
live under ignored `eval/results/2026-09-20-lsp-reliability/`; failures are preserved.
Edge artifacts ending `T011903.257719000` and `T012018.071555000` are the initial
18 calls. Those ending `T012523.802709000` and `T012601.517739000` are the focused
eight. `strap-lsp-symbol-*.jsonl` logs distinguish schema, initial traversal,
grounded traversal, edge and final-description phases. Source snapshots and a
manual-review record are retained alongside the raw data.

Offline verification passed `go test ./...`, `go vet ./...`, `go build ./...`, and
`go test -race ./lsp ./tool ./harness ./internal/lspconfig`. The symbol resolver has
24 table cases plus mixed-target checks. Real-server race tests passed for Go,
Rust, Python, JavaScript, TypeScript and Bash, including the new identifier-target
inspection. Real-model edge/workflow qualification here is Go-only; it does not
establish equivalent model behavior for the other languages or larger projects.
