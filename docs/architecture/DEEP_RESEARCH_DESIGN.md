# Deep research tool

Status: proposal, 2026-09-17. Nothing here is implemented. Records the deadline
audit, the external survey, and the proposed contract so the implementation can
be split into independently testable checkpoints the way
[RESEARCH_IMPLEMENTATION.md](RESEARCH_IMPLEMENTATION.md) was.

A `deep_research` call treats a question as an investigation rather than a
lookup: it decomposes the question, gathers evidence across several passes, and
returns a structured report in which every material claim is traceable to a
source it actually read. A single call is expected to run for minutes, not
seconds, and the runtime currently has no contract that tolerates that.

## 1. Deadline audit

### 1.1 The 29–30 second wall (removed 2026-09-18)

This section describes the state before `0e7c4c2` was reverted. The stall
watchdog, its budgets, its CLI flags and the agent's stall retry are all gone;
there is currently **no stream liveness guard at all**, and agent liveness is
being approached from a different direction. The analysis is retained because
it is why the feature was removed rather than widened, and because the
remaining rows of 1.2 are still live.

The observed limit was `DefaultStallIdle` in `harness/model.go`, 25s of
mid-stream silence, reported between 25.0s and 31.25s because the watchdog
sampled on a fixed grid rather than measuring the gap directly. The tick came
from the budgets:

    interval = max(min(FirstChunk, Idle)/4, 250ms)
             = max(min(2m, 25s)/4, 250ms)
             = 6.25s

The ticker started with the stream, so a gap beginning at an arbitrary moment
was only noticed at the next tick at or after 25s. The detected quiet period
was therefore uniform over [25.0s, 31.25s), mean ~28.1s. That is the reported
"29-30 seconds". The agent then repeated the call twice (`maxStallRetries`)
before the error escaped, so the user-visible delay was roughly three times
that.

The watchdog did **not** run during tool execution, and this remains true of
anything that replaces it. The outer model's response has completed before
`invokeCall` dispatches, and the agent imposes no deadline on a tool. A
synchronous multi-minute tool is not killed by a stream-level policy; the model
calls it makes internally would have been.

### 1.2 Every deadline a long research run crosses

| Where | Current | Behaviour at deep-research scale |
| --- | --- | --- |
| ~~`harness.DefaultStallIdle`~~ | ~~25s, trips 25–31s~~ | **Removed.** Synthesis over a large context on a loaded server paused longer than this |
| ~~`harness.DefaultStallFirstChunk`~~ | ~~2m~~ | **Removed.** Covered prefill *and* queue wait; parallel scouts multiply the queue |
| `ModelConfig.Timeout` | 60m | `http.Client.Timeout` spans the whole body read, so one long stream counts against it |
| `WebConfig.OpenTimeout` | 30s | Clock starts before the concurrency slot is acquired |
| `Web.openSlots` | capacity 2 | Caps page reads at two at a time |
| `WebConfig.SearchTimeout` | 20s | Adequate for the search API |
| `ResearchExecution.Timeout` / `MaxTimeout` | 30s / 60s | Researcher diagnostic shell; unrelated but adjacent |
| `eval/runner.go` Idle | 3m | Safe: `watcher.busy()` counts an in-flight tool |

### 1.3 The `open_url` queue defect

[open_url.go](../../tool/open_url.go) calls `w.begin(ctx, OpenTimeout)` before
selecting on `w.openSlots`. Queue wait and read budget share one 30s allowance,
so with a fan-out above the slot capacity of two, the excess calls spend their
entire budget waiting in line and fail as deadline errors with nothing actually
wrong. This is a defect for any parallel consumer, independent of this feature.

Splitting it needs a separate `QueueTimeout` and a read deadline that starts
after the slot is held, plus a configurable `OpenConcurrency`.

### 1.4 Raising the budgets is not a fix

Enlarging `StallIdle` buys headroom, not a contract. A budget large enough for
the worst legitimate pause is also large enough to hide a genuine hang, and any
number chosen is a guess about a distribution that changes with context length,
server load and concurrency. There are research scenarios where five minutes of
quiet is legitimate, and a five-minute silence budget is not a liveness check.

What the runtime needs instead is a positive signal that work is progressing,
so silence is interpreted against evidence rather than against a clock. Treat
the budget changes below as a stopgap that keeps the feature testable, not as
the design. The liveness protocol is tracked separately.

## 2. External survey

Sources are listed in section 6.

**Orchestrator and workers is the consensus shape.** A lead agent decomposes the
question, parallel subagents search and evaluate independently, a synthesis pass
combines them. Two production findings matter for the contract here: subagents
must return structured results with an explicit success or failure status
because the orchestrator has to parse them, and token spend alone explained
about 80% of performance variance. Budget is the primary quality dial, so it
belongs in the arguments and in the returned record.

**Citation checking is a separate pass.** Anthropic runs a distinct citation
agent over the raw documents and the finished report. An agent that wrote a
claim is a poor judge of whether its source supports it; the same-agent version
degrades into a game of telephone.

**Fan out only where the task decomposes.** Parallelism wins on genuinely
independent sub-questions and wastes tokens on sequential ones.

**Fix the brief before spending.** Hosted products insert a clarifying turn
because a long run cannot be steered once started. A tool cannot ask a
follow-up, so the schema should force the caller to state acceptance criteria
and required coverage before the budget is committed.

**Report shape.** Executive summary, key findings, evidence table,
disagreements, risks and unknowns, recommended actions, sources. Readers scan
purpose, jump to the recommendation, then check findings to see whether the
recommendation is earned. Contradictions belong in their own section rather
than averaged away; stating them transparently strengthens rather than weakens
a report.

**Evaluation is already defined; borrow it.** DeepResearch Bench separates RACE
(comprehensiveness, insight, instruction adherence, readability) from FACT
(citation accuracy, effective citations per task). The two instrumentable
metrics are faithfulness, whether a cited source supports its claim, and
groundedness, what fraction of the report is cited at all. Both fit the
existing eval ladder.

## 3. Proposed tool

### 3.1 Placement

Register `deep_research` on the researcher role in
[workflow/session.go](../../internal/workflow/session.go), not on the root.

Tool calls dispatch serially on the calling agent's own goroutine, so a
multi-minute call freezes that agent's inbox for the duration. On the root that
is unacceptable. On a researcher it is correct: blocking on one bounded
question is the role's whole job, and the root stays responsive to the user.
Placing it there also removes the main argument for an asynchronous start/poll
handle, which would otherwise need a job registry and a wake signal.

### 3.2 Arguments

Flat by construction. [parameters.go](../../tool/parameters.go) already records
that a string arriving where an array or object belongs is the signature of the
vLLM XML parser failing on nested structure, so the contract avoids arrays
inside objects.

```go
type deepResearchArgs struct {
	Question        string   `json:"question"`
	Context         string   `json:"context,omitempty"`
	SuccessCriteria []string `json:"success_criteria"`        // required: the acceptance test
	MustCover       []string `json:"must_cover,omitempty"`    // dimensions that must appear
	AllowDomains    []string `json:"allow_domains,omitempty"` // flat, not nested under `sources`
	BlockDomains    []string `json:"block_domains,omitempty"`
	Depth           string   `json:"depth,omitempty"`         // survey | standard | exhaustive
	MaxMinutes      *int64   `json:"max_minutes,omitempty"`
}
```

Constraints in the existing idiom:

```go
builtin("deep_research", deepResearchDescription, d.run,
	MinLength("question", 1), MaxItems("success_criteria", 8), MinLength("success_criteria[]", 1),
	MaxItems("must_cover", 12), MaxItems("allow_domains", 32), MaxItems("block_domains", 32),
	Enum("depth", "survey", "standard", "exhaustive"),
	Minimum("max_minutes", 1), Maximum("max_minutes", 20),
	Reject("", "sources", "domain filters are flat: allow_domains / block_domains"),
	Reject("", "query", "the field is question"),
)
```

### 3.3 Pipeline

| Stage | Responsibility |
| --- | --- |
| plan | Decompose into at most 8 independent sub-questions with search terms |
| scout | Parallel workers: search, triage hits, read pages, return structured findings |
| reconcile | Cluster findings, detect contradictions, mark unresolved conflicts |
| synthesize | Write the report against `success_criteria` and `must_cover` |
| verify | Separate pass over raw sources and the draft, per-claim supported or not |

A scout returns `{claim, basis, confidence, evidence[], status}`, never prose.
`verify` receives the captured page text, never a scout's summary; a claim that
fails verification is demoted to `inferred` or dropped with a recorded
limitation.

### 3.4 Return shape

Reuse the ledger vocabulary already in [work/research.go](../../work/research.go)
and [tool/progress.go](../../tool/progress.go): `basis` of observed, inferred or
retracted, `evidence` as objects carrying a `uri`, plus `open_questions`,
`recommendation` and `proposed_steps`. A researcher can then pipe findings
straight into `report_work_progress` and `submit_research` without translation.

```go
type DeepResearchReport struct {
	ReportID      string         `json:"report_id"`
	Status        string         `json:"status"` // complete | partial | budget_exhausted | cancelled
	Summary       string         `json:"summary"`
	Findings      []Finding      `json:"findings"`
	Disagreements []Disagreement `json:"disagreements,omitempty"`
	OpenQuestions []string       `json:"open_questions,omitempty"`
	Limitations   []string       `json:"limitations,omitempty"`
	Coverage      Coverage       `json:"coverage"` // criteria met/unmet, must_cover addressed
	Spend         Spend          `json:"spend"`    // elapsed, tokens, searches, fetches
	Truncated     bool           `json:"truncated"`
	NextCursor    string         `json:"next_cursor,omitempty"`
}
```

Two constraints on the result:

- **Bound what reaches model history.** `tool.Result` already separates
  `Captured`, the complete host retention, from `Content`, what enters history.
  The full report goes in `Captured`; `Content` carries a bounded digest and a
  `report_id`. A `get_research_report(report_id, cursor)` reader follows the
  `next_cursor` convention `open_url` established. Research briefs cap at
  64 KiB today and a deep report will exceed that routinely.
- **Always return something.** Cancellation or an exhausted budget returns
  `status: "partial"` with what was gathered, not an error. The agent already
  appends tool content on `ctx.Err()`, and retained partial output on
  cancellation is the established pattern from research checkpoint 13.

### 3.5 Configuration

```go
type DeepResearchConfig struct {
	Enabled         bool
	Model           ModelConfig   // separate provider instance with its own stall budgets
	DefaultDuration time.Duration // 6m
	MaxDuration     time.Duration // 20m, enforced as the tool's own deadline
	MaxSubquestions int           // 8
	Scouts          int           // 4
	MaxSearches     int           // 40
	MaxFetches      int           // 60
	MaxReportBytes  int           // 256 KiB
}
```

Supporting changes:

1. ~~Stall budget tuning.~~ Moot: the stall feature was removed entirely on
   2026-09-18 and no stream liveness guard replaced it. See
   [ADR-004](ADR-004-stream-liveness.md) for what was tried and why it was
   rejected. One correction from that work is still worth carrying: the long
   legitimate waits in a research run are queue and prefill, not mid-generation
   pauses, so whatever replaces this should not assume a research model call
   goes quiet mid-stream for minutes.
2. **Split the `open_url` deadline** per section 1.3. Still open.
3. **Raise the page-read concurrency** for the research path, or give the
   research runtime its own `*Web` with a higher slot count. Still open.

Items 2 and 3 are small, independently testable, and worth doing regardless of
how the tool lands, and neither depends on the liveness question.

### 3.6 Progress visibility

A multi-minute tool currently publishes a start notification and then nothing
until it finishes. An optional host-facing sink on `tool.Call`:

```go
type Call struct {
	InvocationID string
	Arguments    json.RawMessage
	Actor        message.ActorID
	Sender       message.Sender
	Progress     func(Update) // nil when unobserved; must not block
}
```

`agent.invokeCall` supplies a closure publishing through a new `OnToolProgress`
alongside `OnTool`. It never enters model history, so it costs no tokens, and it
keeps `tool` free of workflow dependencies the way `SubmitResearch` does by
taking an injected handler. This sink is also the natural carrier for whatever
liveness signal section 5 settles on.

## 4. Open questions

**Sub-agents or bare provider calls for scouts.** The pipeline above runs scouts
as direct provider calls with the web tools passed in. Spawning real
`agent.Agent` instances would give them the full tool loop, recording and
interrupt semantics already hardened here, at the cost of entangling `tool` with
`agent`. Current lean: bare provider calls, because a scout's job is narrow
enough that the agent loop is mostly overhead.

**Eval before implementation.** A FACT-style faithfulness and groundedness
scorer built against fixture reports is the only way to tell whether the verify
pass earns its tokens, and it is cheap to build first.

## 5. Liveness, tracked separately

Section 1.4 rejects the timeout-widening approach as the long-term answer. The
replacement is a liveness contract: the runtime should abandon a call because
nothing is progressing, not because nothing has arrived recently.

[ADR-004](ADR-004-stream-liveness.md) attempted the stream half and was
rejected. Its audit records why: stream liveness says nothing about whether a
multi-minute *tool* is progressing. A scout stuck retrying, or a deadlock on
the two-slot browser semaphore, produces healthy streams and no research.
Rather than carry a watchdog that cannot answer the question, the existing one
was removed and the problem reopened.

What a replacement has to cover is the tool half: the progress sink in section
3.6, carrying a monotonic step counter and stage name so a host can separate
"scout 3 of 8, slow fetch" from "hung". That remains unbuilt and is the
prerequisite for running `deep_research` unattended.

## 6. Sources

- [How Anthropic built a multi-agent research system](https://blog.bytebytego.com/p/how-anthropic-built-a-multi-agent)
- [Anthropic's multi-agent research architecture explained](https://theaiengineer.substack.com/p/how-anthropic-built-multi-agent-deep)
- [Architecture lessons from production](https://cuizhanming.com/anthropic-multi-agent-research-architecture/)
- [Deep research, OpenAI API guide](https://developers.openai.com/api/docs/guides/deep-research)
- [Deep research FAQ, OpenAI Help Center](https://help.openai.com/en/articles/10500283-deep-research-faq)
- [Deep Research, Prompt Engineering Guide](https://www.promptingguide.ai/guides/deep-research)
- [DeepResearch Bench II, hierarchical report rubrics](https://github.com/imlrz/DeepResearch-Bench-II)
- [ResearcherBench, evaluating deep AI research systems](https://arxiv.org/pdf/2507.16280)
- [Deep-Research Eval, quality and reliability in long-form reports](https://www.mdpi.com/2076-3417/16/5/2546)
- [Redesigning and auditing deep research writing for faithful reports](https://arxiv.org/html/2608.28643)
- [Deep research agent, enterprise guide and architecture 2026](https://www.ampcome.com/post/deep-research-agent)
- [Executive summary structure](https://libguides.usc.edu/writingguide/executivesummary)
