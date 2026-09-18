# Revision conflict recovery experiment

Date: September 16, 2026, America/Los_Angeles. Artifact timestamps use UTC.

## Result

**Qwen3.8-27B recovered in 5/5 live trials, on the first retry each time.**
All five trials exercised the real revision guard and preserved domain state on
rejection. The live harness checks passed 80/80 assertion instances, 16 per trial.
These repeat a narrow set of properties; they are not 80 distinct contracts.

The scripted suite passed 5/5 scenarios and 51/51 harness assertion instances,
including the new race case at 17/17. Unlike the earlier live baseline, this
experiment actually forced rejection and observed recovery.

## Intervention and expected transition

The new scenario is `audit-revision-race`. It uses the same repaired arithmetic
fixture, root prompt, coordination tools, and normal audit-assignment request as
the original independent-audit case. The model receives no warning that a race
will be injected.

The runner waits for an otherwise valid audit assignment from the model. Before
returning that response to production tool dispatch, it assigns and cancels a
competing audit through public host operations. The original work changes from
`needs_check` revision 7, through `checking` revision 8, back to `needs_check`
revision 9. The submission and plan remain unchanged. One cancelled audit remains
in the ledger as evidence of the competing operation.

The model's original request is dispatched unchanged with expected revision 7.
The production harness rejects it against revision 9. A correct retry must use
current state, create exactly one active audit, and leave the original work at
`checking` revision 10. The experiment uses a deterministic interleaving of real
operations; it does not simulate the conflict by replacing a tool receipt or by
editing the model's arguments.

## Observed behavior

All five traces followed the same sequence:

1. `get_work` returned revision 7 and the latest submission.
2. The host assigned and cancelled the competing audit, advancing the revision to 9.
3. `assign_work(expected_revision=7)` received a real stale-revision error.
4. The root consumed the new review notification containing current revision 9.
5. `assign_work(expected_revision=9)` succeeded.

No trial made another `get_work` call. Both the conflict error and the fresh review
notification supplied revision 9 before the retry. We cannot attribute recovery
to one of those inputs alone. This is valid use of updated information, but it
does not demonstrate an explicit reread or isolate error-message-only recovery.

| Measure | Result |
| --- | ---: |
| Scorable live trials | 5/5 |
| Successful recoveries | 5/5 |
| Required conflict rejections | 5 |
| Additional rejected calls | 0 |
| Unknown tool / output / provider errors | 0 |
| Model requests | 15 |
| Tool calls | 15 |
| Median trial duration | 5.89 s |
| Trial duration range | 4.82–7.15 s |
| Summed trial duration | 29.12 s |
| Reported input tokens | 154,823 |
| Reported output tokens | 1,294 |

The report labels these as recovered successes, not clean successes, because each
contains one rejection. That rejection is the forced experimental challenge; it
does not indicate an avoidable initial decision error. Token totals include
resubmitted context, not unique context or measured uncached compute.

Independent projection of the raw trace changes confirmed that plans, submissions,
prior audit records, progress records, and preexisting other work were unchanged.
Exactly two audit children were added per trial: the cancelled host intervention
and the active audit assigned by the model. The saved trace also reproduces the
graded state.

## Scope and next question

This is a positive result for bounded recovery from a revision conflict with
unchanged audit content. It remains a five-trial sample of one guided fixture,
using a live root actor and controlled implementor/auditor providers. It does not
evaluate audit judgment or recovery from an arbitrary concurrent edit.

The next useful experiment should **replace the latest submission during the
interleaving**. Updating only `expected_revision` would then be insufficient:
the model would need to notice the changed submission and bind its audit to the
new outcome. That distinguishes revising a version number from reconciling the
meaning of changed state. No prompt or model settings change is justified by the
current result.

## Reproduction and implementation checks

The live server was the configured Qwen endpoint at `http://model.internal:8360`,
using profile `qwen3.8-27b-nothink`. Thinking was disabled, temperature 0.7, top-p
0.8, top-k 20, min-p 0, presence penalty 1.5, repetition penalty 1, and output
limit 4,096 tokens. The trial budget was eight model requests and three minutes;
each model request had a 90-second timeout.

```sh
/private/tmp/strap-interaction-race interaction run \
  -mode live -config internal/modelcatalog/models.json \
  -profile qwen3.8-27b-nothink -scenario audit-revision-race -repeat 5 \
  -max-tokens 4096 -timeout 3m -model-timeout 90s -max-calls 8 \
  -out eval/results/interaction-revision-race-20260917T034220138Z/live
```

The recorded binary identifies its source as `6b2fbdd-dirty`; its SHA-256 is
`711b5225fe17ba35b0a14bccc70a04726b9a69e62b4235d227a229bef81da22c`.
Use a fresh output directory when repeating the command. Build the current runner
from `./cmd/strap-eval`; the temporary executable above is the archived run's build.

The implementation and CLI tests, race detector, and `go vet` passed for
`./eval/interaction` and `./cmd/strap-eval`. Tests cover real conflict receipts,
fresh reads, unchanged tool arguments, intervention ordering, bounded stale
retries, premature completion, corrupted intervention state, and unrelated actor
actions. Final review also hardened cancelled-output handling and repeated
provider call IDs; those cases have dedicated regressions and did not occur in
the five recorded live traces.

## Saved evidence

- [Live report](../../eval/results/interaction-revision-race-20260917T034220138Z/live/report.md)
- [Scripted report](../../eval/results/interaction-revision-race-20260917T034220138Z/scripted/report.md)
- [Metrics and per-trial tool sequences](../../eval/results/interaction-revision-race-20260917T034220138Z/analysis-metrics.json)
- [Run identity](../../eval/results/interaction-revision-race-20260917T034220138Z/live/run.json)
- [Usage guide](../../eval/interaction/README.md)

Each result includes intervention cursors, the unmodified triggering request,
before/after work, and the cancelled audit. Per-trial directories preserve the
full traces and manifests. The result directories are ignored local artifacts;
retain them when sharing this analysis.
