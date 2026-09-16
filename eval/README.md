# Harness evaluation ladder

`strap-eval` runs the harness against a ladder of 60 coding tasks (20 easy,
20 medium, 20 hard), records one JSONL trace per task exactly as `strap -record`
would, grades each attempt with hidden tests, and summarizes the traces.

Each task wraps the algorithmic insight of a LeetCode problem in an ordinary
engineering request: a small Go module with a stub, a README that states the
contract, and a user message asking for the implementation. The agent never
sees the hidden tests, so it has to read the repository and verify its own work.

## Commands

```sh
go build -o strap-eval ./cmd/strap-eval

./strap-eval list                                   # every task with its insight
./strap-eval selfcheck                              # hidden tests fail on the stub, pass on the reference
./strap-eval run -tier easy -parallel 2             # record eval/results/<timestamp>/
./strap-eval run -tier easy,medium,hard -parallel 2 -profile PROFILE -out eval/results/RUN
./strap-eval run -out eval/results/<dir>            # rerun the same directory to resume
./strap-eval report eval/results/<dir>              # write report.md and report.json
```

`run` accepts the same model flags as `strap` (`-config`, `-profile`,
`-base-url`, `-model`, generation overrides). `-task id,id` and
`-tier easy,medium,hard` select tasks. Omit `-tier` for all tiers. Whitespace
and duplicate tiers are accepted; unknown or empty tier names are errors.
`-parallel N` runs N sessions at once within a tier. Tiers always run in
easy → medium → hard order, with each tier finishing before the next starts.
`-quiet` sets how long the session must stay silent after the root's
final reply before the attempt is considered finished (default 3s).

`run` writes `report.md` and `report.json` after successful completion; use
`-report=false` to skip report generation. The multi-tier command above replaces
the former `run-eval.sh` script, which is removed. From another directory, also
pass `-ladder /path/to/strap/eval/ladder`.

## Run layout

A run directory defaults to `<commit>_<profile>_<timestamp>`: the harness
build's short git hash (`-dirty` when the tree had uncommitted changes,
`nogit` when the binary carries no VCS information), the model profile, and
the start time. Runs therefore sort by harness build first, then by model.
Both values are also recorded in `run.json` and printed in the report.

```
eval/results/<run>/
  run.json              model, ladder, task list
  results.jsonl         one Result per finished task, appended as tasks finish
  report.md, report.json
  <task-id>/
    trace.jsonl         the session recording (same format as strap -record)
    workspace/          the agent's module, plus the hidden tests copied in afterwards
                        (during the session it lives under the system temp directory)
    result.json         outcome, grade output, final root reply, capture health
```

Interrupting a run leaves finished tasks in place; rerunning with the same
`-out` reuses every task that already has a `result.json`.

While a session runs, its workspace lives in a fresh directory under the
system temp directory (`-scratch` overrides the parent), not under `-out`. An
agent that explores upward from its working directory therefore finds other
temporary workspaces at most, never the repository with the ladder's hidden
tests and reference solutions. The workspace moves under `-out` after grading.

Keep runs that feed one write-up together in a bundle directory named after
that write-up, for example
`eval/results/2026-09-15-three-model-comparison/<run>/`, with a `README.md`
that lists the runs and links the reports under `docs/evals/`. Run directories
can be moved: `report` looks for each trace beside its `result.json` under the
run directory, not at the path recorded when the task ran. Tier runs that
share a profile can be merged into one run directory by concatenating their
`results.jsonl` files and moving the task directories together.

## Completion rule and grading

A task attempt is finished when the root agent has sent a reply to the user and
nothing is still in flight: no agent is running, no tool call is open, and no
message is queued for a live agent. Those are the same signals the TUI uses for
its activity indicator. The attempt then has to stay silent for the quiet
period. A session whose agents are all idle with nothing queued and no root
reply for the idle period (`-idle`, default 3 minutes) is finished early and
flagged `no_reply`; a root that ends its turn with `wait_for_input` instead of
a reply would otherwise cost the whole budget. If the tier's session budget
runs out first (15, 25 or 40 minutes by default, overridable per task), the
session is closed and the result is marked `timed_out`. In every case the
workspace is still graded.

Grading copies `hidden/` into the workspace and runs
`go test ./... -count=1 -run ^TestHidden`. The agent's own test files still
compile, so a broken test breaks the build the way it would for a person.
Outcomes are `passed`, `failed`, `build_failed` and `error` (infrastructure
problem; nothing to grade).

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
- `strap-eval selfcheck` must pass: every hidden suite fails on the stub and
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

## What the first smoke runs showed

Two live attempts at `easy-01-budget-pair` against qwen3.6 (`qwen3.6-coding`
preset) illustrate what the report surfaces:

- One attempt passed in 41 seconds with 5 model calls. The root read the README
  and stub, wrote the solution itself, ran `go build` and `go vet`, and replied.
  It never created a plan or delegated, which the `agents`, `roles` and `work
  events` columns make visible.
- The other attempt stalled: the third model call streamed 370 KB of reasoning
  for almost 15 minutes and was cancelled when the session budget ran out. The
  `longest call`, `reasoning KB` and `failed outputs` columns show that pattern.
  The profile's generation settings allow very long outputs; pass `-max-tokens`
  or `-thinking=false` to `run` when you want to bound that rather than measure it.
