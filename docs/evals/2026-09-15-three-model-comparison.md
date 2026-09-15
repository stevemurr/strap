# Ladder comparison: qwen3.6 thinking, qwen3.6 no-thinking, nemotron-lightning

Same 60-task ladder, same harness commit (`1b6aba7`), same runner settings
(`-parallel 2`, budgets 15/25/40 min by tier). Earlier write-ups:
[qwen3.6 baseline](2026-09-14-qwen3.6-ladder.md) and
[qwen3.6 vs nemotron](2026-09-15-qwen3.6-vs-nemotron-lightning.md).

| run | settings | results dir | tasks with a result |
|---|---|---|---|
| qwen3.6 thinking | preset `qwen3.6-coding`, thinking on, max_tokens 131072 | `eval/results/2026-09-15-three-model-comparison/qwen3.6-thinking` | 60 |
| qwen3.6 no-thinking | same preset with `enable_thinking: false` | `eval/results/2026-09-15-three-model-comparison/qwen3.6-nothink` | 60 (hard-18 timed out in a `create_plan` retry loop) |
| nemotron-lightning | preset none, temperature 1.0, top_p 0.95, thinking on | `eval/results/2026-09-15-three-model-comparison/nemotron-lightning` | 59 (hard-19 was interrupted) |

Two instrument defects surfaced during these runs and are fixed in the ladder
now: hidden-test helpers named `replay` (easy-20) and `sortedKey` (medium-02)
collided with helpers in the agents' own code and broke the build. Every
unexported helper in every hidden test is now prefixed `hidden`, and the full
selfcheck passes. "Corrected" below re-grades those two workspaces with the
renamed tests; both solutions pass.

## Headline numbers

| measure | qwen3.6 thinking | qwen3.6 no-thinking | nemotron-lightning |
|---|---|---|---|
| passed, as graded | 47/60 | 55/60 | 55/59 |
| passed, corrected | 48/60 | 56/60 | 55/59 |
| easy / medium / hard (corrected) | 20 / 13 / 15 | 20 / 17 / 19 | 19 / 18 / 18 |
| tasks that produced no code in the graded workspace | 9 | 2 | 0 |
| tasks that hit the session budget | 10 | 2 | 7 |
| of those, the work was already done and passed | 1 | 1 | 7 |
| wall clock | 7.2 h | 3.9 h | 4.8 h |
| healthy task mean, easy / medium / hard | 1.6 / 3.3 / 4.3 min | 0.9 / 4.2 / 3.7 min | 0.9 / 1.6 / 1.9 min |
| output tokens per task, easy / medium / hard | 4.2K / 11.4K / 39.1K | 1.8K / 6.9K / 7.9K | 3.8K / 6.9K / 9.8K |
| input tokens per task, easy / medium / hard | 130K / 176K / 221K | 94K / 657K / 913K | 117K / 177K / 247K |
| tasks delegated to worker agents | 20 | 11 | 0 |
| audits (verdict fail) | 17 (0) | 14 (2) | 0 |
| reasoning loops that never emitted a tool call | 9 | 0 | 0 |
| tool errors | 54 | 575 | 51 |
| longest streak of identical failed tool calls | 5 | 422 | 2 |
| tasks that ran `go test` | 31/60 | 43/60 | 35/59 |

Per-task overlap: no task failed under all three. The two qwen modes share
medium-10 (same missed error case, same audit pass). nemotron's three
performance failures (easy-13, medium-03, hard-20) passed under both qwen
modes. The nine tasks qwen lost to reasoning loops all passed without
thinking.

## What turning thinking off changed for qwen3.6

Removing the reasoning channel removed the dominant failure: nine tasks that
produced nothing became nine passes, and the run took 3.9 hours instead of
7.2. Output tokens per hard task fell from 39K to 7K. No completed call
exceeded 4 KB of output.

It did not remove looping; it moved it into tool calls:

- **Retrying an identical malformed call.** `create_plan` was rejected 534
  times across the run, 527 of them sending `steps` without the required
  `title`. hard-18 repeated the same rejected call 422 times in a row and
  timed out without writing any code; medium-20 47 times, hard-02 27, hard-04
  13. The error text
  names the missing field every time. Thinking mode's longest streak was 5.
- **Editing the same plan step forever.** medium-13 called `edit_step` 156
  times, 140 of them byte-identical apart from the revision number, on one
  step, then hit the budget without replying. The code was written and passes.

Correctness stayed the same or improved. Direct-mode work was verified far
more often: 26 of 48 direct tasks wrote a test file and 32 ran `go test`,
against 9 and 15 of 40 in thinking mode. The two remaining logic failures are
medium-10 (identical bug and identical audit pass to thinking mode: empty
targets return before deps are validated) and medium-20 (the auditor rejected
the first submission for not tracking cumulative expansion size, the repair
still lets exactly 10,000,001 bytes through). Audits rejected work twice
with substantive findings, the first rejections seen in any run.

One task is invalid rather than failed. In medium-14 the root ran `find` on
the repository root, discovered `eval/ladder/medium/14-segment-hashtag/`,
wrote its solution into the ladder's own workspace copy, read the hidden test
and the reference solution there, and reported completion. The graded
workspace still held the stub. No other task in any run touched the ladder.
The ladder stub has been restored from git.

## What each model's failures look like

| | qwen3.6 thinking | qwen3.6 no-thinking | nemotron-lightning |
|---|---|---|---|
| dominant loss | reasoning loop before the first tool call (9 tasks, 4.8 h) | identical-call retry loop (hard-18, medium-20, hard-02, hard-04) and a plan-edit loop (medium-13) | finishing with `wait_for_input` instead of a reply (7 tasks, 3.6 h idle) |
| logic bugs | 2 (medium-09 union-find, medium-10 empty-targets) | 2 (medium-10, medium-20 boundary) | 0 |
| performance misses | 1 (hand-rolled insertion sort) | 0 | 4 (hand-rolled swap sorts, linear-scan heap, per-cluster allocation) |
| audits | all pass, one wrong | 2 of 14 rejected with real findings | none |
| delegation | 20 of 60 | 11 of 59 | never |
| recovery from server errors | reassigns work, reports failure cleanly | recreated a dead auditor and passed | n/a |

## Harness and runner issues these runs exposed

1. **Workspaces live inside the repository.** `eval/results/` sits under the
   repo, so `find` from the task directory reaches the ladder with its hidden
   tests and reference solutions. Materialize workspaces under a temp root
   (or anywhere outside the source tree) and keep only traces and results
   under `-out`.
2. **`wait_for_input` with a finished summary ends the exchange silently.**
   Seven nemotron tasks and one qwen task paid the full budget for a completed
   job. Treat a substantive final text with no active work as the reply, or
   reject the call and re-prompt.
3. **Schema rejections are not self-correcting without thinking.** 534
   `create_plan` rejections for one missing field. Either accept `steps` alone
   with a derived title, or have the rejection include a minimal valid example.
4. **Plan-edit and reasoning loops both need a circuit breaker**: cap
   identical consecutive tool calls, cap reasoning bytes per call, and finish
   a task when all agents are idle with accepted work even without a reply.
5. **Audit depth.** Only the no-thinking run rejected anything. Requiring one
   new test against the README contract would have caught medium-10 in both
   qwen runs and every nemotron performance miss.

## Recommendation for the next round

Run qwen3.6 without thinking as the default configuration for this harness,
with an identical-call circuit breaker, and re-run only the seven nemotron
silent tasks after the `wait_for_input` fix to get its true time cost. Move
workspaces out of the repo before any further run.
