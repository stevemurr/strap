# Agent loop: proposed code changes

Status: **proposal only, not applied**. Prepared September 17, 2026 against the current working tree. This directory changes no runtime or test source. It rectifies the eight confirmed findings in the [architecture review](../AGENT_LOOP_ARCHITECTURE_REVIEW.md).

The larger changes below include exact replacement types, functions, and call-site edits; they are not represented as complete, compiled patches. The four smaller fixes are also provided as an [unapplied unified diff](policies.patch).

## Review map

| Finding | Proposed change | Exact code |
| --- | --- | --- |
| 1. Dead dispatcher accepts new work | Retain its terminal error, seal the existing admission gate, and let the harness's existing finalizer close execution. | [Dispatcher supervision](dispatcher.md) |
| 2. Model truncation excluded from scoring | One typed output-failure contract; runtime output facts drive eval classification. | [Output failures and validation](provider.md) |
| 3. Injected providers bypass invariants | The same completion validator runs before history acceptance and tool dispatch for every provider. | [Output failures and validation](provider.md) |
| 4. Large records lose failure/correlation | One bounded tool header shared by inline records, framed records, projector, and inspection. | [Event records](event-records.md) |
| 5. Repeat counter spans unrelated requests | Move the counter from persistent agent state into an exchange-local variable. | [Exact diff](policies.patch) and code below |
| 6. Audit/repair claims implementation reservation | Update the derived reservation index alongside work replacement, using the same function in live mutation and replay. | [Exact diff](policies.patch) and code below |
| 7. Wait guard ignores older active work | Use the complete owned-work projection instead of the first display page. | [Exact diff](policies.patch) and code below |
| 8. Rejected Run replaces cancellation | Claim Run ownership before installing its cancellation handle. | [Exact diff](policies.patch) and code below |

There are no legacy aliases for the old output-error types and no parallel record decoder. The output and tool-record changes share one schema bump to **6**. Older archives are explicitly unsupported. Tool argument schemas remain owned by the existing tool contracts; the completion validator owns only provider-independent response invariants.

## 5. Give the repeat counter the exchange's lifetime

In `agent/agent.go`, remove `repeated repeatedCall` from `Agent` and declare it alongside the existing recovery counters inside `exchange`:

```go
admitted := false
malformed := 0
overrun := 0
var repeated repeatedCall
```

Replace the repeat accounting in that method with:

```go
if key := a.repeatKey(call, result, err); key == repeated.key {
    repeated.count++
} else {
    repeated = repeatedCall{key: key, count: 1}
}
```

The existing hint and stop checks read `repeated.count`, preserving their thresholds. Delete `a.repeated = repeatedCall{}` from `agent/interrupt.go`; there is no persistent counter to reset.

The counter remains in force across all batches of one exchange. A final reply, yield, or interruption ends that lifetime. Input consumed while the same exchange is still running does not silently reset it. Twelve independent read/reply exchanges no longer look like one runaway loop; twelve identical calls in one uninterrupted exchange still trigger protection.

## 6. Update work and its reservation index together

Add the following to `work/reporting.go`, replacing the current `putWork` implementation:

```go
// Audit and repair scopes never own the implementation's reservation.
func reservationSteps(w Work) []StepID {
    if w.Kind != Implementation || w.State.Terminal() || w.Scope == nil {
        return nil
    }
    return w.Scope.StepIDs
}

// Callers hold s.mu. This updates state without publishing another mutation.
func (s *Store) replaceWork(id ID, v Work) {
    for _, step := range reservationSteps(s.works[id]) {
        if s.reserved[step] == id {
            delete(s.reserved, step)
        }
    }
    v = v.Clone()
    s.works[id] = v
    for _, step := range reservationSteps(v) {
        s.reserved[step] = id
    }
}

func (s *Store) putWork(id ID, v Work) {
    s.replaceWork(id, v)
    s.change.Works = append(s.change.Works, v.Clone())
}
```

In `work/readmodel.go`, replay uses the same update:

```go
for _, w := range c.Works {
    s.replaceWork(w.ID, w)
}
```

In `work/actorstate.go`, remove the independently reconstructed local reservation map and use `ReservedBy: s.reserved[step.ID]`. Remove the scattered reservation writes from `AssignWork`, successful `SubmitAudit`, and `cancelImplementation`; the shared replacement owns all of them. The cancellation path still reads the old reservation to decide which plan steps to reopen before replacing the parent work.

This is one domain rule with a derived index. Audit/repair children cannot take ownership simply because they carry the same scope. Terminal implementation work releases its reservations in both live state and replay.

## 7. Check the complete work projection before waiting

Replace `rootMayWait` in `harness/session.go` with:

```go
func (s *Session) rootMayWait(ctx context.Context, c tool.Call) error {
    view, err := s.workView(ctx)
    if err != nil {
        return nil // Preserve the existing policy when state cannot be read.
    }
    if len(view.ActorState(c.Actor).Owned) != 0 {
        return nil
    }
    return errors.New("wait_for_input rejected: you own no active delegated work, so no worker result can arrive. If the task is finished, send the final reply as a text-only response now; if work remains, assign it first")
}
```

`Owned` already contains only nonterminal work. The existing domain predicate is preserved, but it now sees the whole accepted projection instead of a page capped at 100 items. This does not introduce a broader policy for whether a blocked worker can eventually produce a result.

## 8. Claim Run ownership before changing cancellation state

The start of `Agent.Run` becomes:

```go
func (a *Agent) Run(ctx context.Context) (err error) {
    if !a.started.CompareAndSwap(false, true) {
        return errors.New("agent already started")
    }
    ctx, cancel := context.WithCancel(ctx)
    defer cancel()
    a.reporting.mu.Lock()
    a.reporting.cancel = cancel
    a.reporting.mu.Unlock()
    // Existing run lifecycle follows; remove the later duplicate CAS guard.
```

A rejected second call returns before changing any run-owned state. `RequestStop` therefore keeps the original execution's cancellation handle.

## Regression acceptance criteria

These should be deterministic tests of real boundaries, with no model service required. The provider cases must pass through the real JSON/SSE adapter with an injected HTTP transport, then through runtime acceptance and eval grading. A scripted error alone does not prove that the adapter emits that error.

| Boundary | Required assertion |
| --- | --- |
| Dispatcher read/decode failure | Admission seals; execution cancels; terminal cause remains visible; accepted evidence is readable; normal close remains successful. Include startup/close races. |
| Output classification | Length-truncated output remains scorable; HTTP/stream/capture failure is infrastructure; parent cancellation differs from an HTTP client's own timeout; joined infrastructure errors cannot trigger regeneration. |
| Provider acceptance | Adapter and direct provider accept the same generic completion corpus. A rejected batch dispatches zero tools and commits no assistant message. |
| Event framing | Crossing the framing threshold changes neither tool state, error category/summary, output correlation, nor timestamps. List operations do not hydrate full payloads. |
| Repeat protection | More than twelve independent read/reply exchanges remain healthy; an uninterrupted identical-call loop still stops. |
| Reservations | Live authority, replay, and wake state agree through assignment, audit, repair, acceptance, cancellation, and reassignment. |
| Wait guard | One older active assignment plus 100 newer terminal assignments permits waiting; all-terminal state rejects it. |
| Run ownership | A rejected duplicate Run leaves the original cooperative provider cancelable by RequestStop. |

## Validation performed for this proposal

The four smaller changes were copied into a temporary Go overlay, leaving repository implementation files unchanged. Existing `agent` and `work` package tests passed with that overlay, and the targeted root-wait harness test passed. An independent review found no concrete defect in those four proposed production changes.

The complete `harness` test run reached an HTTP-listener test blocked by the execution sandbox; it did not complete. The new regression cases listed above have not yet been implemented. The larger provider, dispatcher, and bounded-header proposals have not been compiled or integration-tested. No live-server evaluation was run for this proposal.

Implementation order: output contract and validation first, dispatcher supervision second, bounded event header third, then the four lifetime/domain corrections. Each change starts with the corresponding reproduced seam regression and ends with the focused tests above.
