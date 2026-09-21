# Container coding evaluations

Each `strap eval` agent container solves one problem at `/workspace` and publishes
its completed files to an outbox. A separate grader container consumes that
snapshot and runs hidden tests. The agent never receives the private ladder.

## One-command launcher

From the repository root, use Apple's `container` CLI through the host launcher:

```sh
scripts/eval.sh easy-01-budget-pair
scripts/eval.sh --no-build --tier easy -- -profile qwen3.6
scripts/eval.sh --all
scripts/eval.sh --no-build --list
```

The launcher builds the image with caching by default, discovers your host Strap
model catalog, and snapshots it into a fresh batch directory under `eval/results`.
It runs problems sequentially, with a separate agent container and grader container
for each. Private grading fixtures are snapshotted on the host and mounted only in
the grader. Logs, workspaces, submissions and per-problem reports are retained;
the launcher prints their host paths.

Pass multiple problem IDs to run a selection. Use `--config FILE` for another
host catalog, `--out NEW_DIRECTORY` for an explicit batch location, and place
model flags after `--`. The endpoint in the catalog must be reachable from the
container. `--no-build` uses the existing image; omit it after source changes.
Existing output directories are rejected, so retries cannot overwrite results.
These are host-launcher options; paths inside every container remain fixed.

Infrastructure failures are logged, remaining problems still run, and the launcher
exits nonzero. A failed solution is a completed grade and does not cause a nonzero
exit; read each `results/<problem-id>/result.json` for the outcome. Interrupting the
launcher stops its active container and keeps artifacts. `scripts/eval.sh --help`
lists all options. No model requests are made by `--list`.

## Mount contract

| Path | Agent container | Grader container |
| --- | --- | --- |
| `/problems` | Public task metadata and starter files, included in the image | Unused |
| `/workspace` | Empty working directory; all agent tools and language servers use it | Fresh working directory for a copy of the submission plus hidden tests |
| `/results` | Writable mounted traces, run metadata, results and reports | Same results mount, writable for grade and reports |
| `/outbox` | Empty writable mount; publishes `submission/` when ready | Same outbox mount, **read-only** |
| `/grading` | **Not mounted** | Full private task ladder, **read-only** |

The CLI fixes these paths. There are no `-scratch`, `-out`, `-ladder`, `-task`,
`-tier`, or `-parallel` options for agent runs, no automatic result reuse, and
no workspace relocation. Each retry needs fresh workspace, results and outbox
directories. Run multiple containers to evaluate problems in parallel.
The Go embedding API accepts an explicit `Mounts` value for tests; it uses the
same workflow and checks that active mounts exist, are directories and do not overlap.

## Build and run

Use Apple’s `container` CLI on macOS. Build from the repository root:

```sh
container build -f eval/Dockerfile -t strap-eval .
container run --rm strap-eval list
```

The image contains Go and gopls (versions are set in `eval/Dockerfile`), Bash,
ripgrep and Poppler. It contains only public problem fixtures; private hidden
tests and reference solutions exist only in the build stage and the explicit
smoke-test target, never in the runtime image.

Create a fresh attempt directory on the host. Supply a model endpoint reachable
**from the container**; `localhost` refers to the container itself. Replace the
example model and endpoint below with your server settings.

```sh
attempt="$PWD/eval/results/container-attempt-001"
mkdir -p "$attempt/workspace" "$attempt/results" "$attempt/outbox"
container run --rm --progress none \
  --mount "type=bind,source=$attempt/workspace,target=/workspace" \
  --mount "type=bind,source=$attempt/results,target=/results" \
  --mount "type=bind,source=$attempt/outbox,target=/outbox" \
  strap-eval -problem easy-01-budget-pair -q \
  -backend chatcompletions -base-url http://MODEL_HOST:8000/v1 -model MODEL_NAME
```

`-config`, `-profile`, generation overrides, and LSP flags use the same model
configuration as `strap`. To use a host catalog, mount it read-only and pass its
container path to `-config`. Go language tools start gopls inside the container.
Other language servers must be installed in a derived image if needed.
`run -problem ID` is also accepted; omitting the `run` subcommand is shorthand.

After the agent container exits successfully, grade the ready submission:

```sh
container run --rm --progress none --network none \
  --mount "type=bind,source=$attempt/outbox,target=/outbox,readonly" \
  --mount "type=bind,source=$attempt/results,target=/results" \
  --mount "type=bind,source=$PWD/eval/ladder,target=/grading,readonly" \
  strap-eval grade -q
```

The grader uses the image's empty `/workspace`; do **not** mount the original
agent workspace there. The shipped ladder uses only the standard library, so
grading needs no network. The grader never contacts a model. Starting another
fresh grader container with the same outbox and results reruns grading.
A host scheduler can watch attempt directories, wait for the agent container to
exit, and dispatch this command when `outbox/submission/manifest.json` exists.
There is no embedded daemon or Docker socket dependency.

## Submission and results

After closing the harness and LSP processes, the runner copies the workspace to
`/outbox/.pending/workspace`, writes a versioned manifest, then renames `.pending`
to `submission` within the same mount. Only `submission` is ready. Cancellation
or failed copying never publishes a partial submission. Snapshots contain regular
files and directories; symlinks and special files fail submission explicitly.
The manifest records the session result and run metadata, identifying one problem
and one session. The grader verifies the results mount belongs to that session.

```
workspace/                         original agent work, retained in place
outbox/submission/
  manifest.json                    version, run metadata and ungraded result
  workspace/                       submitted snapshot, unchanged by grading
results/
  run.json                         model, build, problem and mount metadata
  results.jsonl                    one result; grade replaces submitted outcome
  report.md, report.json
  <problem-id>/
    trace.jsonl                    full agent session recording
    result.json                    submitted outcome, then grading result
```

The agent phase reports `submitted`, not passed or failed. Infrastructure failures
return a command error and publish no ready submission. Interrupted attempts retain
the workspace and trace but publish no submission. A timed-out or idle session can
still submit its partial solution after orderly shutdown, retaining `timed_out`
or `no_reply` in the manifest.

`grade` copies the snapshot into its own `/workspace`, overlays hidden tests from
`/grading`, and runs `go test ./... -count=1 -run '^TestHidden'`. Agent-authored
Go test files must also compile. Outcomes are `passed`, `failed`, `build_failed`,
and `error`. A failing solution is a completed grade (exit zero); command and
infrastructure errors exit nonzero. Read `result.json` for the grading outcome.
Run one grader per attempt at a time; this single-submission protocol does not
implement distributed claims or concurrent result writers.

## Commands and progress

- `strap eval -problem ID -q` runs and submits one solution, printing a short summary.
- `strap eval grade -q` grades a ready submission and writes updated reports.
- `strap eval report` rebuilds reports from the `/results` mount.
- `strap eval web [-results DIR] [-ladder DIR] [-listen ADDR] [model flags]` serves a local
  page for reading, comparing and analysing runs under a results directory, and
  runs tasks from the page on this host with live progress when a ladder is present.
- `strap eval list [-tier easy,medium,hard] [-task ID,...]` lists public problems.
- `strap eval selfcheck [-tier ...] [-task ...] [-parallel N]` checks private fixtures;
  mount the full ladder at `/grading`. This utility runs no agent.

`-ui auto` chooses the read-only progress TUI for terminals, otherwise plain logs.
`-ui plain` forces logs; `-q` or `-ui quiet` suppresses progress but retains the
summary and errors. `-q` overrides `-ui tui`. `-report=false` skips the agent's
ungraded execution report; grading always writes reports.
The TUI retains the shared tool activity, plan and agent inspection controls.

A session completes when the root has replied and all work and tool calls have
settled for `-quiet` (default 3s). This delay is independent of `-q`.
`-idle` (default 3m) bounds an idle session without a root reply. Per-problem
session budgets are unchanged (15/25/40 minutes by tier unless overridden).

## Validation

```sh
go test ./eval ./internal/evalcmd ./internal/tui
container build -f eval/Dockerfile --target smoke -t strap-eval-smoke .
```

The container smoke target needs no model server. Linux CI builds the same
Dockerfile with Docker. A scripted provider exercises
shell working-directory discovery, relative and absolute reads, real gopls,
submission publication and hidden-test grading at the actual container paths.
Ordinary tests use isolated mount directories and cover cancellation, immutable
submissions, regrading, mount overlap and preservation of existing files.

The separate [interaction suite](interaction/README.md) evaluates bounded
coordination and schema decisions. Its fixture-oriented commands and artifact
layout remain independent of coding-problem submission and grading.

## Task format

```
eval/ladder/<tier>/<NN-slug>/
  task.json        id, tier, title, insight, prompt, optional timeout/test_timeout
  workspace/       the module the agent sees: go.mod, README.md, the stub file
  hidden/          *_test.go files with TestHidden* functions, copied in at grade time
  reference/       a correct solution whose file name matches the stub it replaces
```

Conventions:

- The stub keeps the exported signature and panics with `not implemented`, so
  the hidden tests fail on an untouched workspace.
- `README.md` in the workspace is the contract: context, exact signature,
  semantics and tie-breaks, an examples table, and constraints including the
  input sizes the hidden tests use. The prompt is a short, realistic request
  that points at README.md and the file to edit.
- Hidden test functions are named `TestHidden*`. Performance tests run the
  function in a goroutine and fail after a deadline, so an inefficient solution
  fails deterministically instead of hanging the binary. Randomized tests use a
  fixed seed and compare against a brute-force oracle in the test file.
- Standard library only; `go 1.24` in `go.mod`.
- `strap eval selfcheck` must pass: every hidden suite fails on the stub and
  passes on the reference.

The ladder directory holds its own `go.mod` marker so the fixture modules stay
outside the main module's `go build ./...`.

## Report

`report` reads `results.jsonl` and every `trace.jsonl`. Per task it counts model
calls and tokens (usage events), the peak measured context size, tool calls and
tool errors by tool name, agents by role, work events and audit verdicts, root
replies and time to first reply, failed model outputs and agent exits. Per tier
it reports pass rate, timeouts, mean duration, mean model and tool calls, and
mean tokens. Failures list the session or capture error and the tail of the
grade output.
