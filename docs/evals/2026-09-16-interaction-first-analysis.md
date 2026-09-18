# First focused interaction evaluation

Date: September 16, 2026, America/Los_Angeles. Run timestamps are UTC.

## Finding

The current Qwen3.8-27B no-thinking profile completed **20/20 live audit
assignments cleanly**, across five repetitions of each pilot scenario. The
scripted baseline also passed all four cases, including the three deliberately
invalid assignments and their corrections.

This establishes a useful baseline for **guided audit assignment and argument
binding**. It does not establish autonomous delegation quality, audit judgment,
or reliable recovery from concurrent state changes. The current live cases are
easy enough that none triggered a rejection.

## What actually ran

| Component | Execution |
| --- | --- |
| Root agent | Real model requests to the configured server at `http://model.internal:8360`, using the chat-completions API |
| Model | Server advertised `qwen3.8-27b`; profile `qwen3.8-27b-nothink` |
| Prompts and tools | Production root prompt, coordination tool schemas, decoding, dispatch, and workflow operations |
| State | A real harness session, with a scoped arithmetic implementation, failed first audit, repair, and replacement submission seeded through public APIs |
| Implementor and auditor | Controlled, blocked providers; they do not call the model during the measured interaction |
| Local shell, files, web, research execution | Disabled for these coordination trials |
| Stop condition | Complete batch that assigns the audit, before another model decision or an auditor verdict |

The server's model-list response identified its owner as `sglang`. The
configured `vllm` adapter is the compatible client path; that adapter name does
not mean this server is running vLLM.

The default CLI mode is scripted and does not contact a model. These measured
trials explicitly used `-mode live`.

```sh
/private/tmp/strap-interaction-analysis interaction run \
  -mode live -config internal/modelcatalog/models.json \
  -profile qwen3.8-27b-nothink -repeat 5 \
  -max-tokens 4096 -timeout 3m -model-timeout 90s -max-calls 8 \
  -out eval/results/interaction-first-analysis-20260917T031823746Z/live
```

The binary was built once from `6b2fbdd-dirty` and used for both the scripted and
live runs. Its SHA-256 was
`6351d4bf7d196f98a533548db958c300a28e623c3c9ef0a61d7f696df365433b`.
All 20 live manifests contain identical model settings: thinking off, temperature
0.7, top-p 0.8, top-k 20, min-p 0, presence penalty 1.5, repetition penalty 1,
and a 4,096-token output cap. Trials ran sequentially without changing prompts,
schemas, grading rules, or generation settings during the batch.

## Results

| Scenario | Live correct | Clean | Recovered | Model calls | Tool calls | Mean trial time |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Independent audit | 5/5 | 5/5 | 0 | 10 | 10 | 5.70 s |
| Wrong-assignee handoff | 5/5 | 5/5 | 0 | 9 | 9 | 3.20 s |
| Superseded-submission handoff | 5/5 | 5/5 | 0 | 10 | 12 | 3.88 s |
| Stale-revision handoff | 5/5 | 5/5 | 0 | 8 | 8 | 3.72 s |
| **Total** | **20/20** | **20/20** | **0** | **37** | **39** | **4.12 s** |

All 20 trials were scorable. There were no provider errors, timeouts, rejected
tool calls, unknown tool errors, or output errors.

The scripted baseline passed **4/4 cases and 34/34 assertion instances**. Each
negative script made one invalid request, observed rejection with unchanged
domain state, then successfully corrected it. The exact assignee, submission,
and revision guards were exercised once each.

The live batch passed **120/120 assertion instances**, six per trial. Those
checked the audit transition and binding, preservation of other state and
immutable records, absence of acceptance without a verdict, and replay
consistency. They are repeated checks of a small set of properties, not 120
different contracts. **No negative guard was exercised by the live model.**

## Trace analysis

Every trial created exactly one audit bound to the latest submission, left the
original implementation in `checking`, and left plan completion unchanged.
There were no extra agents or unrelated successful mutations in the measured
interval.

The completed tool sequences were:

| Tool sequence | Trials |
| --- | ---: |
| `get_work -> assign_work` | 15 |
| `assign_work` | 3 |
| `get_work -> get_work_progress -> assign_work` | 1 |
| `get_work -> list_agents -> assign_work` | 1 |

The three direct assignments were wrong-assignee trial 3 and stale-revision
trials 3 and 5. They were valid: the model's wake context already supplied the
current work revision and latest submission. Requiring a redundant read would
measure one preferred tool sequence rather than correctness.

The two extra inspections occurred in superseded-submission trials 1 and 4.
They caused no adverse state changes. This sample does not establish whether
those reads improve reliability.

The `audit_id` versus `submission_id` schema confusion observed during the
earlier implementation smoke test did not recur in this batch. That earlier
mistake remains covered by a deterministic recovery regression; its absence here
is not evidence that the model can never repeat it.

## Cost and latency

The trials used 82.50 seconds of summed session time, with a median of 3.69 seconds
and a range of 2.43–8.74 seconds. They averaged 1.85 model requests and 1.95 tool
calls per trial. This excludes building the binary and the server health check.

The provider reported 368,777 input tokens and 2,963 output tokens, with usage
available for all trials: approximately 18,439 input and 148 output tokens per
trial. Input totals include context submitted again on subsequent requests;
they are not unique context size or a measurement of uncached compute. These
numbers suggest that production prompt/tool/history context dominates the token
volume of this small decision. We did not isolate cache behavior, warm-up, or
server load, so differences in trial latency should not be attributed to a
particular cause.

## Interpretation and next experiments

The prompts explicitly identify an eligible auditor, label misleading handoffs
as wrong or outdated, and instruct the model to use current records. All four
scenario labels are variations on the same repaired arithmetic fixture.
Five repetitions per case assess narrow consistency, not broad reliability.
A 20/20 result on this fixture is a baseline, not a production reliability estimate.

The wrong-assignee case also combines two constraints: the proposed reviewer is
both a contributor and an implementor. Its rejection cannot isolate contributor
independence from role eligibility.

The runner stops after the successful assignment batch. Passing does not show
that the model would voluntarily stop, wait correctly, report completion
truthfully on a later turn, or choose a correct audit verdict. The auditor never
executes a review in this pilot.

The next experiments should be:

1. **Force a real revision conflict.** Advance work through valid host operations
   after the model reads it and before assignment. Require rejection without
   side effects, a refresh, and one successful correction. This tests actual
   feedback-driven recovery rather than avoidance of an explicitly stale hint.
2. **Reduce the hints and vary the fixture.** Use held-out wording, multiple
   plausible actors and submissions, and outdated handoffs without labeling
   them as outdated. Keep expected transitions fixed.
3. **Evaluate a live auditor.** Provide tiny known-good, known-bad, and
   insufficient-evidence artifacts. Check verdict and evidence separately from
   whether the verdict operation is legal.
4. **Extend through repair and re-audit.** Observe the full interaction after
   assignment, including waiting, failed verdicts, repairs, and the final
   acceptance state.

There is no evidence from this batch that a prompt change is needed. The highest
value next step is making the cases more discriminating, then comparing a targeted
change against this unchanged baseline.

## Artifacts

- [Live report](../../eval/results/interaction-first-analysis-20260917T031823746Z/live/report.md)
- [Scripted report](../../eval/results/interaction-first-analysis-20260917T031823746Z/scripted/report.md)
- [Analysis metrics](../../eval/results/interaction-first-analysis-20260917T031823746Z/analysis-metrics.json)
- [Live run identity](../../eval/results/interaction-first-analysis-20260917T031823746Z/live/run.json)
- [Pilot usage guide](../../eval/interaction/README.md)

Each run directory includes per-trial fixtures, manifests, results, and full
session traces. Results are local artifacts under the ignored `eval/results/`
directory; preserve that directory if sharing or archiving the analysis.

