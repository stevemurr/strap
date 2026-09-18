# ADR-004: Stream liveness is measured in generation, not in bytes

Status: **rejected, 2026-09-18.** Proposed 2026-09-17 as a replacement for the
two-budget byte-counting stall policy added in `0e7c4c2`. It was implemented
and green, then discarded: rather than replace the watchdog, the whole stall
feature was reverted, and agent liveness is being approached from a different
direction. Neither `chatwire/stall.go` nor the code below exists in the tree
any more.

Kept as a record, not as a plan. Two things in it outlive the code: the latent
defect described in the third paragraph of Context, which was real and shipped,
and audit finding 1, which is the reason this direction was abandoned.
Motivated by [DEEP_RESEARCH_DESIGN.md](DEEP_RESEARCH_DESIGN.md).

## Context

`chatwire.StallPolicy` bounded a streaming response with two budgets,
`FirstChunk` and `Idle`, both measured by `stallReader` counting bytes off the
wire. `Idle` defaulted to 25s.

Three problems, in increasing order of seriousness.

**The budget did not mean what it said.** The watchdog sampled on a fixed grid
whose interval was `max(min(FirstChunk, Idle)/4, 250ms)`, which for the shipped
defaults is 6.25s. A gap beginning at an arbitrary moment was only noticed at
the next tick at or after 25s, so the detected silence was uniform over
[25.0s, 31.25s), mean ~28.1s. Reports of a "29 to 30 second" limit were reading
the sampling error, not the budget. `agent.go` then repeats a stalled call
twice, so the user-visible delay was roughly three times that.

**Bytes are not generation.** Servers emit SSE keep-alive comments so that
intermediaries do not tear down a quiet connection; ALB, GCLB and nginx all
default to a 60s idle timeout, OpenAI sends empty SSE comments for this reason,
and vLLM ships `--sse-keep-alive-interval`. Those comments are bytes.
`stallReader.Read` stamps its clock on any bytes at all, so enabling that flag
on the model server would have satisfied the watchdog permanently: a generation
that stopped producing would run to the 60-minute `http.Client.Timeout` instead
of being abandoned. The parser already discards non-`data:` lines, so this was
one server flag away from happening silently.

**The same conflation broke the first-chunk budget.** `delivered` flipped on
the first byte. A keep-alive comment arriving during prefill would therefore
retire the 2-minute `FirstChunk` budget and hand a slow prefill to the 25s
`Idle` budget — turning the flag that was supposed to protect long waits into
the thing that killed them.

Underneath all three is one mistake: a transport-liveness probe was being used
as a work-liveness probe. Widening it does not fix it. A budget large enough
for the worst legitimate pause is also large enough to hide a real hang, and
the number is a guess about a distribution that moves with context length,
server load and concurrency.

## Decision

Split the single measurement into three budgets that measure different things,
and move the progress measurement above the SSE parser so it can tell a
generation chunk from a keep-alive comment.

```go
type StallPolicy struct {
	FirstChunk time.Duration // Wait for the first generation chunk. Zero disables.
	Transport  time.Duration // Wait between bytes, keep-alives included. Zero disables.
	Progress   time.Duration // Wait between generation chunks. Zero disables.
}
```

| Budget | Measures | Detects | Default |
| --- | --- | --- | --- |
| `FirstChunk` | Time to the first decoded chunk | Queue wait and prefill outgrowing their allowance | 2m |
| `Transport` | Byte silence, keep-alives included | A dead connection | disabled |
| `Progress` | Gap between decoded chunks | A generation that stopped producing | 90s |

`Transport` defaults to disabled deliberately. Without server keep-alives, byte
silence and chunk silence are the same measurement, so a transport budget would
only duplicate `Progress` at a shorter deadline and reintroduce the false
positive `Progress` exists to remove. Enable it together with a server
keep-alive interval, at two to three times that interval, which is the standard
heartbeat-timeout ratio.

Every chunk is also bytes, so chunk silence is always at least byte silence and
a `Transport` budget above `Progress` can never be reached. `Validate` rejects
that pair rather than letting it read as a working setting.

### Two clocks, one watchdog

`stallWatch` would have replaced `stallReader` and carries both clocks. `Read` stamps the
byte clock. `mark` stamps the chunk clock and is called by the stream decoder,
the only layer that can distinguish generation from a comment.

```go
type stallWatch struct {
	body      io.Reader
	lastByte  atomic.Int64
	lastChunk atomic.Int64
	chunked   atomic.Bool
}
```

`overrun` evaluates the budgets in force at a given instant. The first-chunk
budget is keyed on `chunked`, not on bytes, which is what makes it survive
keep-alives during prefill:

```go
func (w *stallWatch) overrun(now time.Time, policy StallPolicy) *StallError {
	if budget := policy.Transport; budget > 0 {
		if quiet := now.Sub(time.Unix(0, w.lastByte.Load())); quiet >= budget {
			return &StallError{Stage: StalledTransport, Quiet: quiet, Budget: budget, Delivered: w.chunked.Load()}
		}
	}
	budget, stage := policy.FirstChunk, StalledBeforeFirstChunk
	if w.chunked.Load() {
		budget, stage = policy.Progress, StalledMidGeneration
	}
	if budget <= 0 {
		return nil
	}
	if quiet := now.Sub(time.Unix(0, w.lastChunk.Load())); quiet >= budget {
		return &StallError{Stage: stage, Quiet: quiet, Budget: budget, Delivered: w.chunked.Load()}
	}
	return nil
}
```

Transport is checked first so that when both have lapsed, a connection that
stopped delivering anything is reported as the more specific explanation.

### What counts as progress

This is the part that only became clear by writing the code. The obvious design
was to wrap `provider.Observer`, which `client.go` already threads into
`readStream`, and treat every `OnDelta` as progress. That is wrong:
`stream.go` emits deltas for content and reasoning only. **A model streaming
tool-call arguments produces no `OnDelta` at all**, and in an agentic harness
that is the most common shape of a response. Wrapping the observer would have
abandoned healthy tool calls.

The progress signal is therefore taken inside `consume`, where the decoded
choice is in scope, and covers every payload a generation chunk can carry:

```go
if progress != nil && (reason != "" || c.Delta.Content != "" || len(c.Delta.Calls) > 0 || c.Finish != nil) {
	progress()
}
```

Frames with no choices — usage-only records and the `{"choices":[]}` heartbeats
some servers emit — return earlier in `consume` and never reach this, so they
correctly do not count as progress.

### Honest budgets

The sample interval is now bounded above as well as below:

```go
return min(max(smallest/4, 250*time.Millisecond), 2*time.Second)
```

Slop falls from a quarter of the smallest budget to at most two seconds, so a
25s budget reports at 25s rather than somewhere under 31.25s, and a five-minute
budget does not become proportionally vaguer.

A resettable `time.AfterFunc` would give exact detection with O(1) wakeups
while idle, but it would cost a timer reset on every decoded chunk — thousands
per response. The bounded ticker trades a fixed 0.5Hz wakeup for no per-chunk
work, which is the better side of that trade for a streaming decoder.

### Stage on the error

`StallError` carries which budget was exceeded, because the three mean
different things to a caller. `Stage.Retryable()` reports false only for
`StalledMidGeneration`: a call that never started or whose connection died
committed nothing and cost little, while a generation that produced tokens and
then stopped has already spent its budget once.

`chatwire` is internal to `provider`, so callers outside it match the
capability rather than the type, in the style of `net.Error`:

```go
var retryable interface{ Retryable() bool }
if errors.As(err, &retryable) && !retryable.Retryable() { /* do not repeat */ }
```

## Implementation state

Built and verified, then discarded. At the time of writing it was clean under
`go build ./...`, `go vet ./...` and `go test -race -count=2 ./provider/...`.
The table below records what it touched; none of it is in the tree now, and
neither is the feature it modified.

| File | Change |
| --- | --- |
| `provider/internal/chatwire/stall.go` | Rewritten: three budgets, two clocks, bounded tick, staged error |
| `provider/internal/chatwire/stream.go` | `readStream` takes a `progress func()`; marked in `consume` |
| `provider/internal/chatwire/client.go` | Wires the watch as reader and `watch.mark` as progress |
| `provider/vllm/client.go`, `provider/chatcompletions/client.go` | Validate the policy at construction |
| `harness/model.go` | `StallIdle` becomes `StallProgress`; adds `StallTransport`; new defaults |
| `internal/modelcatalog/catalog.go` | `-stall-idle` becomes `-stall-progress`; adds `-stall-transport` |
| `stall_budgets_test.go`, `stall_keepalive_test.go` | New: 6 cases covering interval bounds, validation, budget handover, keep-alives, tool-call fragments |

`TestKeepAliveCommentsDoNotHideAStuckGeneration` is the regression the old
design could not catch, and it demonstrates the separation in one assertion: a
200ms *byte* budget survives indefinitely against a server sending keep-alives
every 20ms, while a 400ms *chunk* budget correctly abandons the stuck
generation.

## Audit of this proposal

Written against the implementation above, not against the intent.

**1. The change fixes the measurement, not the scope.** This is the most
important finding, and in the end it is the one that sank the ADR. The problem that prompted the work is a research tool
running for minutes across many model calls and web fetches. Stream liveness
says nothing about whether that *tool* is progressing: a scout stuck retrying,
or a deadlock on the two-slot browser semaphore, produces perfectly healthy
streams and zero research progress. This ADR is necessary and not sufficient.
The tool-layer contract — a `Progress` sink carrying a monotonic step counter
and stage name, so a host can distinguish "scout 3 of 8, slow fetch" from
"hung" — is §3.6 of the deep research design and is still unbuilt.

**2. `Stage.Retryable()` is defined and unused.** `agent.go:344` still repeats
every `ErrStreamStalled` up to twice. With `Progress` at 90s, a genuine
mid-generation hang now costs 270s rather than the ~90s the budget implies, and
the repeat re-spends a budget already spent once on the same suspected cause.
The wiring is three lines and is shown above. It was left out because changing
retry behaviour is a separate decision with its own blast radius, but shipping
the method unconsumed is dead code and should not survive the commit that adds
it. Either wire it or drop it.

**3. 90 seconds is still a guessed constant.** The ADR criticises guessed
budgets and then picks one. The defence is that 90s is roughly 45x the largest
mid-stream pause in recorded generation, and that the number now governs a
measurement that means what it claims. But it is a constant, and the adaptive
step — phi accrual over observed inter-chunk arrivals, bucketed by model and
context length, which is what Cassandra and Akka do for exactly this
short-timeout/long-timeout tradeoff — is not implemented. Until it is, the
system is better calibrated but not self-calibrating.

**4. Detection latency regresses for the common case.** The old effective floor
was ~25-31s; the new one is 90s. For an ordinary agent turn that is a real
loss: a hung stream now takes three times as long to surface. The trade is
deliberate, since the old floor was producing false positives, but it should be
measured against the eval ladder rather than assumed, and the number revisited
with data.

**5. With `Transport` disabled, a dead socket is misreported.** It surfaces as
`StalledMidGeneration`, because chunk silence is all that is being measured.
The diagnosis is wrong even though the outcome is right. This is an accepted
cost of not enabling server keep-alives, but it means the default configuration
cannot actually distinguish the two failures the ADR is built around — the
separation only becomes real once `--sse-keep-alive-interval` is on. The ADR
should not be described as delivering that distinction by default, because it
does not.

**6. A `finish_reason` frame refreshes the progress clock.** A server that
sends `finish_reason` and then hangs before `[DONE]` buys one extra `Progress`
budget before being abandoned. Bounded and minor, but it is a case where the
progress signal is slightly more generous than "the model is still generating".

**7. `WithStallPolicy` documents a precondition it does not enforce.** It tells
the caller to validate first and then accepts anything. Both adapters do call
`Validate`, and `chatwire` is internal so those are the only callers, but a
comment is a weaker guarantee than a signature. Returning an error would be
honest.

**8. Breaking config surface.** `stall_idle_ns` becomes `stall_progress_ns`,
and `-stall-idle` becomes `-stall-progress`. No catalog profile on disk and no
script sets either, so the migration cost here is zero today, but any external
profile would fail silently — an unknown JSON field decodes to the zero value,
which then takes the default. A rejecting decoder, or a retained deprecated
alias, would turn that into a visible error.

**9. Test coverage gaps.** There is no case for `Progress` disabled, none for
the `StallTransport` default flowing through `ModelConfig.Resolve`, and none
asserting the new defaults end to end against a live-ish server. The unit
tests exercise `overrun` directly, which is the right granularity, but
`Resolve` coverage is one table row and should be added.

**10. Timer wakeups increase for long budgets.** Bounding the tick at 2s means
a 10-minute `FirstChunk` now wakes 300 times where it previously woke 8.
Negligible per request, but it scales with concurrency, and deep research is
explicitly a fan-out workload. Worth a glance under load before committing.

## Outcome

Finding 1 decided it. Making the stream watchdog measure the right thing would
still have left the actual problem untouched: nothing in it can tell whether a
multi-minute tool is progressing, which is the failure that matters for a
research run. Carrying a more elaborate watchdog that does not answer the
question is worse than carrying no watchdog, so `0e7c4c2` was reverted in full
and this ADR was not implemented. The consequences below describe what would
have followed had it shipped, and are retained for whoever revisits this.

## Consequences (had it shipped)

Accepted: detection latency for ordinary turns roughly triples (finding 4); the
config surface breaks (finding 8); the default configuration still cannot
separate a dead socket from a stuck generation until server keep-alives are
enabled (finding 5).

Gained: budgets that mean what they say to within two seconds; a `FirstChunk`
budget that can safely be raised to absorb queue and prefill for a fan-out
workload without also widening the mid-generation budget; immunity to the
silent failure that enabling `--sse-keep-alive-interval` would otherwise have
introduced; and a per-stage error that lets a caller decide whether repeating
is worth it.

Next, in order: wire or drop `Retryable` (finding 2); close the `Resolve`
coverage gap (finding 9); enable `--sse-keep-alive-interval` server-side and
set `Transport` to two or three times it, which is what makes finding 5 go
away; then build the tool-layer step-counter heartbeat (finding 1), which is
the part that actually addresses long-running research. Phi accrual (finding 3)
last, and only if the fixed budgets prove insufficient with data behind them.

## Sources

- [Detecting activity failures, Temporal](https://docs.temporal.io/encyclopedia/detecting-activity-failures)
- [Long-running activity: tracking progress and handling cancellation with heartbeats, Temporal](https://docs.temporal.io/design-patterns/long-running-activity)
- [The four types of activity timeouts, Temporal](https://temporal.io/blog/activity-timeouts)
- [Phi accrual failure detection algorithm](https://arpitbhayani.me/blogs/phi-accrual/)
- [Heartbeats in distributed systems](https://arpitbhayani.me/blogs/heartbeats-in-distributed-systems/)
- [Lifeguard: local health awareness for more accurate failure detection](https://arxiv.org/pdf/1707.00788)
- [AI agent monitoring: thresholds, liveness, and escalation](https://www.openlegion.ai/en/learn/ai-agent-monitoring)
- [How to detect when an AI agent is stuck or looping](https://www.agentcenter.cloud/blogs/how-to-detect-agent-stuck-or-looping)
- [Why LLM agents fail silently in production](https://dev.to/robat_das_3c6e956212f6408/why-llm-agents-fail-silently-in-production-and-how-to-detect-it-1a58)
- [vLLM OpenAI entrypoints, SSE keep-alive](https://docs.vllm.ai/en/latest/api/vllm/entrypoints/openai/)
- [litellm: per-deployment SSE keepalive for long streams](https://github.com/BerriAI/litellm/pull/30058)
- [AIP-151: Long-running operations](https://google.aip.dev/151)
