# Agent loop architecture review

Reviewed September 17, 2026 against the current working tree, including the
schema changes and the newer session-interruption implementation. This is an
audit and proposed remediation order, not an accepted redesign. No runtime code
was changed during the review.

## Conclusion

There are six architectural gaps worth addressing, plus two smaller correctness
defects. Several repeat the schema problem: the producer, consumer, and evaluator
each implement a slightly different version of a contract. Other gaps put a
policy at the wrong lifetime or treat a required background component as an
optional observer.

| Priority | Boundary | Confirmed consequence |
| --- | --- | --- |
| P1 | Dispatcher failure → session health/admission | Session accepts work after its dispatcher has exited; assignments never reach workers. |
| P1 | Provider outcome → recovery/eval classification | Token-limit output is classified as infrastructure failure and excluded from model scores. |
| P2 | Provider implementation → runtime acceptance | A direct provider can dispatch duplicate call identities rejected by the built-in adapter. |
| P2 | Event framing → inspection | Large failed tool results lose their error in tool summaries. |
| P2 | Exchange progress → persistent agent lifetime | Twelve separate legitimate requests can trigger the runaway-loop stopper. |
| P2 | Work reservation → model wake snapshot | Audit/repair scopes compete with their implementation for the reported reservation owner. |

P1 findings undermine session liveness or evaluation integrity. P2 findings have
confirmed triggers but narrower impact. None of the proofs requires a live model.

## 1. Required dispatcher failures are not supervised

The workflow reader discards an error from `Controller.NextEvent` and closes its
channel. The dispatcher treats that as shutdown, sets its own `closing` flag, and
exits. It does not report the cause to the harness or close the shared admission
gate. When that gate exists, workflow mutation admission does not check the
private flag.

Evidence: [reader exit](../../internal/workflow/session.go#L235),
[dispatcher exit](../../internal/workflow/session.go#L450), and
[mutation admission](../../internal/workflow/operations.go#L273).

**Reproduction:** inject one read error into the real harness's workflow event
reader. Afterwards the session remains `open`, capture health has no error, the
root is still running, `Send` succeeds, and `AssignWork` returns an active work
item. The worker stays idle because no dispatcher remains to deliver it. This
is distinct from required publication failure, which already has a failure path.

**Architectural fix:** give required background components an explicit terminal
result owned by the session. Distinguish normal shutdown from unexpected read or
decode failure. Unexpected dispatcher failure must latch a visible session error,
fence new mutations, and cancel affected execution. Existing accepted work must
remain inspectable. Do not silently convert failure into an empty event stream.

**Regression seam:** inject a read/decode failure after startup; assert session
health reports the cause, new commands are rejected, execution settles, and no
subsequent successful assignment receipt is issued. Test normal close separately.

## 2. Output outcomes lack a shared classification

The wire decoder rejects `finish_reason=length` with a generic error before
examining tool arguments. The same malformed arguments with
`finish_reason=tool_calls` produce `ToolArgumentsError`. Agent recovery and eval
scoring each recognize only particular error types; everything else becomes a
provider failure in the interaction runner.

Evidence: [wire decoder](../../provider/internal/chatwire/wire.go#L145),
[agent recovery](../../agent/agent.go#L317),
[eval classification](../../eval/interaction/runner.go#L165), and
[score exclusion](../../eval/interaction/runner.go#L565).

**Reproduction:** the actual vLLM adapter, an injected HTTP response containing a
length-truncated tool call, and the actual interaction runner produce:

```text
scorable=false outputErrors=0 errorClass="provider" modelCalls=1
```

The malformed-output recovery test injects
`ToolArgumentsError{FinishReason: "length"}`, an outcome the current adapter does
not emit. That test proves handling of its injected error, not handling of the
corresponding wire response. See the
[injected case](../../eval/interaction/adversarial_test.go#L143).

**Architectural fix:** define a typed outcome contract distinguishing transport
failure, rejected model output, truncation, and cancellation. Preserve diagnostic
facts independently of retry policy. Runtime recovery, recorded output status,
and eval scoring should consume the same classification. Whether a token-limit
failure should retry is a separate decision; it must still be classified accurately.

**Regression seam:** actual adapter plus HTTP/SSE fixtures through runtime and
eval scoring. Cover malformed arguments, token limit, empty output, duplicate
identity, HTTP failure, broken stream, and user cancellation. Assert both dispatch
safety and denominator membership. The earlier eight passing live schema trials
did not encounter this defect; broader success-rate claims could be biased by it.

## 3. Runtime completion invariants depend on the provider implementation

The shared Chat Completions decoder checks unique/nonempty call IDs, nonempty
names, and JSON-object arguments. The agent accepts responses from the public
`Provider` interface after checking only for nonempty text or calls.

Evidence: [adapter validation](../../provider/internal/chatwire/wire.go#L159)
and [runtime commitment](../../agent/output.go#L202).

**Reproduction:** a directly injected provider returns two calls with the same
ID. Both calls execute and enter history. The built-in adapter rejects that batch.
Current built-in adapters are protected; scripted providers and future adapters
therefore do not necessarily exercise the same acceptance contract.

**Architectural fix:** centralize provider-independent completion validation at
the runtime acceptance boundary, before assistant-history commitment or tool
dispatch. Adapters retain wire parsing and use the same contract where needed;
do not introduce another independently maintained validation list. Keep malformed
output diagnostics compatible with finding 2.

**Regression seam:** pass the same completion corpus through adapter decoding
and direct provider injection. Require consistent acceptance, no dispatch or
assistant-history commitment for rejected batches, and consistent diagnostics.

## 4. Storage representation changes execution semantics in inspection

Large events are framed into content chunks with a reduced control record. The
tool control record omits failure status. Inspection interprets that reduced
record separately from the full inline record and only sets the tool error for
the inline path. Related reduced history/tool records omit call-correlation data.

Evidence: [reduced tool record](../../harness/eventcodec/publish.go#L191)
and [separate inspection branches](../../harness/inspection/queries.go#L150).

**Reproduction:** publishing the same failed tool result with 10 bytes of content
produces a tool summary with an error. With 70,000 bytes, the summary's error is
nil. Decoding the retained full record still reveals the failure in both cases.
The evidence is retained, but the normal inspection/HTTP summary is misleading.

**Architectural fix:** define one bounded execution header with invocation
identity, output correlation, timestamps, and explicit outcome/error category.
Derive and consume that header for both inline and framed records. Keep large
error detail in content without losing the failure flag. This is particularly
close to the schema issue: two hand-maintained shapes encode one logical fact.

**Regression seam:** representation-invariance tests around the framing threshold.
Changing only payload size must preserve outcome, identity, correlation, and
replay semantics for success, failure, cancellation, and partial execution.

## 5. Runaway-loop protection uses the wrong lifetime

The repeated-call counter lives on the persistent agent. Successful final replies
and fresh user requests do not reset it; interruption does. An autonomous-loop
policy therefore accumulates across unrelated exchanges.

Evidence: [repeat accounting and stop](../../agent/agent.go#L400)
and [interruption reset](../../agent/interrupt.go#L105).

**Reproduction:** eleven independent user requests each perform one identical
read and receive a normal reply. The twelfth read permanently fails the agent for
repeating itself. There is no runaway autonomous exchange in this fixture.

**Architectural fix:** make progress/budget accounting belong to an admitted
exchange. Preserve it across that exchange's tool batches. Explicitly define the
effect of new external instructions consumed during an ongoing exchange; do not
implicitly use the entire agent lifetime as the unit of work.

**Regression seam:** more than twelve independent read/reply exchanges must
remain healthy; an uninterrupted identical-call loop must still stop. Include
repeated legitimate wait/wake cycles and interruption/continuation.

## 6. Wake-state reservations reimplement domain ownership incorrectly

The store reserves scoped steps to their implementation work. `ActorState`
reconstructs reservation owners by iterating all nonterminal scoped work, which
also includes audit and repair children sharing that scope. Map iteration decides
which overlapping work ID wins.

Evidence: [authoritative reservation](../../work/store.go#L370),
[wake-state reconstruction](../../work/actorstate.go#L66), and
[model wake input](../../harness/progress.go#L73).

**Reproduction:** keep one implementation under active audit and read its root
state 1,000 times without a mutation. One run reported the implementation 131
times and the audit 869 times, while the authoritative reservation remained the
implementation throughout. The exact counts vary; the conflicting ownership does
not reflect a real state change.

**Architectural fix:** use one domain reservation rule for mutation checks and
projected state. Audit and repair children must not replace the implementation
as reservation owner. Preserve that rule in live, replayed, and model-facing
views rather than independently inferring ownership from every overlapping scope.

**Regression seam:** compare mutation authority, live state, replay, and wake
snapshots through assignment, audit, repair, acceptance, cancellation, and
reassignment. Identical accepted state must produce identical reservation owners.

## Two smaller confirmed defects

| Defect | Reproduction | Focused fix |
| --- | --- | --- |
| [Wait guard reads only the first page](../../harness/session.go#L472) | One older active assignment plus 100 newer cancelled assignments makes `wait_for_input` claim there is no active work. The second page contains the live assignment. | Use a complete domain predicate or traverse the fixed-prefix result, rather than treating a display page as the whole state. |
| [Rejected duplicate `Run` replaces cancellation ownership](../../agent/agent.go#L210) | Start a blocking cooperative provider, call `Run` again, then request stop. The second run is rejected but the original provider remains blocked in `stop_requested`. | Claim once-only execution ownership before installing run-owned cancellation state. The normal controller calls `Run` once; this affects the exported lower-level API. |

## Recommended implementation order

1. **Unify output classification and completion validation** (2–3), so subsequent
   evals accurately distinguish model failures from infrastructure failures.
2. **Supervise dispatcher failure** (1), with shared session health and admission.
3. **Unify event headers** (4), with payload-size invariance tests.
4. **Correct lifetime and state derivation** (5–6), then the two smaller defects.

Each fix should start with its concrete failing seam test and finish with a
focused interaction eval. The individual changes can remain small; there is no
evidence here that the whole loop needs replacing.

## Scope and evidence

The sweep covered request/response acceptance, streaming and tool dispatch,
history commitment, repeat/recovery behavior, inbox/wake state, workflow delivery,
admission, interruption, event framing, and replay/inspection boundaries.

Existing tests passed for `agent`, `conversation`, `provider/...`,
`internal/workflow`, `work`, `harness/...`, `eventlog/...`, and
`internal/admission`. That green baseline did not cover the counterexamples above.
The audit used temporary overlay tests and standalone fault probes, including the
actual provider adapter with an injected HTTP transport. No live-server sweep or
performance benchmark was run. The review is not an exhaustive correctness proof.

The [local proof archive](../../eval/results/agent-loop-architecture-sweep-20260917/README.md)
contains probe sources, overlay mapping, reproduction commands, and hashes of the
audited source files. It is an ignored local result directory; retain it when
sharing the review. Some probes assert the observed defect and pass; others assert
desired behavior and intentionally fail. They are diagnostic evidence, not tests
to add unchanged to CI.

Deliberate behavior was not treated as a bug: tool batches execute sequentially
without rollback, normal input does not automatically cancel in-flight tools,
wake snapshots are point-in-time observations, and interruption preserves effects
already completed. These match the documented contracts. A related control-path
concern—worker stopping depends on an observer-derived agent list—was identified
but not independently reproduced, so it is not included as a confirmed finding.
