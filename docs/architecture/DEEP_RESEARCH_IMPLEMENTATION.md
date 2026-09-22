# Deep research implementation

Status: experimental, opt-in, 2026-09-22. Implements the
[approved proposal](DEEP_RESEARCH_PROPOSAL.md). Model quality and the benefit of
parallel scouts remain evaluation questions; broad enablement is not approved
by the deterministic tests alone.

## Run it

```sh
go run ./cmd/strap -deep-research
```

Use the existing model configuration and [web dependencies](../../README.md#web-research).
The root creates and assigns a researcher. The researcher calls `deep_research`
with its active `work_id`, a question and 1–8 success criteria. All input keys are
required by the tool contract; `context`, `depth`, `max_minutes`, and `max_tokens`
accept null, and unused domain/topic lists are empty arrays. `depth` is `survey`,
`standard`, or `exhaustive`; null selects standard. `max_minutes` can shorten,
but cannot increase, the selected preset.

The tool is synchronous within the researcher. The root stays available for
input and other work; messaging the researcher does not steer a running call.
Cancel the work, reassign it, interrupt the session, or close execution to stop
it. Accepted evidence is retained, and the result describes why it stopped.

Library hosts set `harness.Config.DeepResearch.Enabled`. `DeepResearch.Model`
can select a dedicated provider configuration; otherwise it inherits the
researcher's provider. Injected `Dependencies.DeepResearchProvider` takes
precedence. `Dependencies.ResearchWeb` supplies typed retrieval for tests or
custom hosts. Injected collaborators must honor context cancellation. Created
web/provider resources remain owned by the session.

## Investigation and retained results

The `research` package implements plan → adaptive scouts → reconcile/follow-up
→ select claims → verify against retained source excerpts → assess coverage.
Independent subquestions can run together; dependencies wait for earlier scouts.
Each scout chooses among bounded search, read and finish actions. These are host
operations, with no arbitrary tool dispatch, shell, files, inbox or delegation.
Malformed stage JSON receives at most one repair; transport failures do not
retry automatically. Failed scouts do not discard evidence from other scouts.

The host assigns source and finding IDs. A citation must contain a unique exact
quote from retained text, with a SHA-256 snapshot hash and UTF-8 byte offsets.
Verification receives claims and original excerpts with surrounding text, not
scout reasoning. Unsupported claims are removed from the final findings; inferred
conclusions require supported premises and an explicit limitation. Summary and
recommendation text is assembled from verified claim IDs, with no unverified
rewrite afterward. A second call to the same model is still fallible.

`tool.Result.Content` is a digest capped at 12 KiB. `Captured` and the final
research event retain the full bounded report; source bodies are separate log
records. Checkpoints can include unverified findings and must not be treated as
final conclusions. Partial reports keep verified findings when available and
state unmet criteria, rejected claims, missing usage and stopping reasons.

The researcher forwards selected evidence through `report_work_progress`, then
uses its ledger-issued finding IDs with `submit_research`. The engine does not
deliver work or write ledger findings automatically. Research finding IDs are
local to a report and cannot substitute for ledger receipts.

## Readers and recovery

`get_research_report`, `Session.ReadResearchReport`, and HTTP
`/sessions/{id}/research-view` share these selectors:

| Mode | Required selector |
| --- | --- |
| `runs` | `work_id` |
| `report`, `sources` | `report_id` |
| `source` | `report_id`, `source_id` |
| `continue` | only `cursor` |

Initial pages accept `max_bytes` from 2–32 KiB (default 16 KiB). Large JSON values
return byte-offset fragments; concatenate their `fragment.text` in order before
decoding the complete JSON. Follow `next_cursor` until absent. Cursors are signed,
actor-bound and pinned to an immutable accepted prefix. Every live continuation
checks current work visibility; a cursor does not retain revoked access.

The live `/trace/research-view` alias uses the same cursor signer. A reused
`inspection.Handler(reader)` serves `/research-view` for trusted archive hosts.
Readers remain available after execution closes, until session disposal.
JSONL archives preserve the feature across process exit; inspection never
restarts scouts. A log without a terminal research event displays `incomplete`
and the last accepted checkpoint. Accepted-log schema 6 adds these events, while
the reader continues to accept schemas 2–5.

## Resource limits

| Preset | Duration | Scouts | Subquestions | Searches | Fetches | Model calls | Follow-up rounds |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Survey | 2 min | 1 | 3 | 8 | 12 | 16 | 1 |
| Standard | 6 min | 2 | 6 | 20 | 30 | 40 | 2 |
| Exhaustive | 15 min | 4 | 8 | 40 | 60 | 80 | 3 |

`research.Config` overrides these ceilings, with at most 20 minutes per run,
four scouts, and one active deep research run per session. Model concurrency
defaults to two and can be reduced to one for a single-slot server. A separate
HTTP client does not isolate a saturated model server from the root.

Acquisition reserves the last quarter of time, model calls and enabled token
capacity for synthesis and verification. Shared reservations happen before
operations start. Hard token budgets require `provider.TokenCounter` plus a
configured `OutputTokenLimiter`; missing reported usage keeps the reservation
charged. Without these capabilities, leave token limits null/zero and use
time/request limits. Reported token counts and conservative charges are separate.

Defaults bound report JSON to 256 KiB, individual source text to 512 KiB, source
text per run to 16 MiB, and retained research payload accounting to 64 MiB per
session. Final-report and captured-result space is reserved before starting.
Payload accounting includes a per-event allowance; it is not a filesystem quota
for framed JSONL or unrelated session events. Model requests, response text and
reasoning are separately bounded at 128, 64 and 192 KiB. Oversized reports become
explicit partial results; cited source records are never evicted to make room.

V1 reads the first returned page selection, retaining at most 24,000 UTF-8 bytes
per source (also limited by request capacity). It records truncation rather than
claiming complete-document coverage. Remote PDF, authenticated browsing, and
query-guided passage selection are outside this implementation. Domain rules
filter initial/final source URLs, with block rules winning; they do not restrict
browser subresource traffic. Retrieved text is untrusted model input.

`WebConfig.OpenConcurrency` defaults to two. `OpenQueueTimeout` defaults to 30
seconds; `OpenTimeout` starts only after a slot is acquired. All reads use the
shared web runtime. Required research records use bounded cancellation-independent
settlement (default five seconds per acknowledgement); storage failure fails
the run and session capture. Arbitrary collaborators that ignore cancellation
cannot be forcibly terminated by Go.

## Validation and quality evaluation

Deterministic tests cover citation provenance, rejection of unsupported claims,
malformed-output repair, limits, atomic token reservations, missing usage,
concurrent scout failure and source deduplication, cancellation, reassignment,
interruption, queue timing, retained partial reports, framed archive replay,
HTTP cursor integrity, authorization changes, and coalesced terminal UI progress.

```sh
go test -race ./research ./tool ./harness/... ./internal/workflow ./internal/tui ./eval/deepresearch
go vet ./...
go build ./...
```

All suites in the command above, static checks, and the full build passed.
The full `go test -race -timeout 20m ./...` run has two failures that also
reproduce from a clean copy of `f48eda5`: `eval/interaction`'s
`TestTimeoutCancelsBlockedProviderAndRetainsResult` expires during setup before
the provider starts, and `internal/evalcmd`'s
`TestResolvedContainerConfigStreamsProgressAndKeepsMounts` fails its recorded
workspace-path comparison. They are not caused by deep research changes.

[Frozen evaluation fixtures](../../eval/deepresearch/testdata/questions.json)
contain twelve synthetic questions and grading notes. The corpus returns fixed
leads, so this measures investigation and verification, not search-engine recall.
The scorer independently validates citation bytes/hashes; faithfulness,
groundedness, coverage and usefulness require independent labels. Missing labels
remain null, and an empty report cannot earn perfect groundedness.

For a bounded live-model smoke test against one frozen case:

```sh
STRAP_DEEP_RESEARCH_EVAL_URL=http://localhost:8000 \
STRAP_DEEP_RESEARCH_EVAL_MODEL=your-model \
STRAP_DEEP_RESEARCH_EVAL_OUTPUT=/tmp/deep-research-eval \
STRAP_DEEP_RESEARCH_EVAL_CASE=comparison-latency \
STRAP_DEEP_RESEARCH_EVAL_REPETITIONS=1 \
go test ./eval/deepresearch -run TestLiveFrozenCorpus -count=1 -v -timeout 10m
```

Omit the case/repetition overrides for twelve cases, one/two scouts, and three
repetitions per condition. Each run is capped at two minutes; allow three hours
for the complete suite. It retains effective model settings, source events,
reports, counters and pre-verification drafts. Draft/final comparison supports
paired grading of verifier filtering; it is not an independent cost measurement
of a verifier-disabled run.

Live-model trials have not yet been run for this implementation. Human calibration,
the existing-researcher baseline, a full verifier-off cost ablation, live-web smoke
coverage, and the proposal's quality thresholds remain release gates. The feature
and parallel mode stay opt-in while those measurements are pending.
