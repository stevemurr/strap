# Public harness session: audited foundation

The proposed successor for streaming and full view reconstruction is
[Streaming and recoverable session views](STREAMING_DESIGN.md), with its revision
audit and [ADR-002](docs/architecture/ADR-002-recoverable-session-log.md).
That design is not implemented yet. It supersedes this foundation's lossy
observation-retention policy for required session records; the implemented
foundation and its original audit remain documented below.

Status: all seven foundation stages are implemented and committed in sequence
from repository baseline `88373f7`: shared commands, session assembly, coordinated
shutdown, event storage, persistent diagnostics, headless telemetry, and HTTP.
The planning audit below records the original findings; implementation results
and remaining limits are described in the stage checklist and final audit.

Planning audit: 2026-09-12. Incorporates agent-bound tools, independent lifecycle
revisions, a replaceable event store, explicit resources, a private provider
transport, and persistent diagnostics. Contracts below supersede earlier sketches.

The decision is recorded in
[ADR-001](docs/architecture/ADR-001-harness-session.md).

## Objective and boundaries

One `harness.Session` is one independently running harness: root, delegated agents,
history, work ledger, configuration, owned resources, and observation history.
The same configuration and commands must follow the same runtime rules through a
TUI, HTTP adapter, or direct Go caller. Model output, timing, and concurrent event
interleavings need not be identical.

```text
TUI                 HTTP adapter                 Eval runner
  \                      |                          /
           public harness.Session
      commands / snapshots / subscriptions
                 |              |
       internal workflow     event publication
           |         |
    conversation    work
         |
      agent -> provider / tool
```

Keep `agent`, `conversation`, `work`, `provider`, and `tool` focused on their
existing responsibilities. `harness` composes them and imports internal workflow;
workflow must not import `harness`. Put shared command contracts below that
dependency boundary, reusing existing domain requests where they fit. Keep
wire encoding out of the domain packages.

`cmd/strap` parses flags, chooses an adapter, and owns process policy. Default
prompts, model setting resolution, role tool selection, inspection-tool behavior,
and resource assembly move behind the shared harness construction path. A CLI may
explicitly close its owned session on quit; a TUI component only detaches.

## Audit findings and corrections

### P1: shutdown can discard the tail of the event stream

[Workflow close](internal/workflow/session.go) calls controller close and then
cancels the workflow reader, including when controller close times out. The
reader and dispatch loop also inherit owner cancellation. Meanwhile,
[inbox receive](inbox/inbox.go) gives cancellation priority over queued events.
Consequently, the proposed promise to drain final events cannot be obtained by
wrapping the current close method.

Separate execution cancellation from dispatch/finalization lifetime. Seal host and
model command admission under a common session gate, cancel execution, and join
already admitted commands and agent loops without holding that gate. Drain the
closed controller stream and publish remaining work events with a live drain
context. In closing mode, dispatch must not provision new agents or initiate new
assignments. Pending work that cannot be delivered remains explicitly unresolved;
it is not acknowledged as consumed or accepted just to finish shutdown.

Close owned execution resources after their users exit. Publish one terminal session
record with shutdown reason and cleanup outcome, then seal the log. Resource errors must
be reported, not encoded as a clean success. A timed-out `Close` only stops that
caller's wait: ownership and the closing process remain intact, and another call
can wait again. A dependency ignoring cancellation prevents confirmed completion.

### P1: a bounded observer log does not bound upstream queues

The controller event inbox and workflow relay both use the unbounded
[inbox implementation](inbox/inbox.go). Adding a ring buffer after those queues
only bounds retained observer history. The work store also has a separate pending
dispatch queue that cannot be evicted like telemetry.

The two current queues serve different purposes; neither is simply an accidental
duplicate. Replace the host relay with session publication when cursor readers
arrive; do not add a third retention layer after it. Keep reliable dispatch separate
from lossy retention. Initially document that controller ingress, agent history,
and pending work remain unbounded. A later admission/backpressure policy must
handle sustained overload before claiming bounded total memory. Neither arbitrary
observer code nor disk/network I/O may run synchronously in execution callbacks.

### P1: snapshots cannot yet be reconciled by a universal revision

[Agent inspection](conversation/controller.go) explicitly returns independent
snapshots. `ContextRevision` counts thread appends; it does not version lifecycle
state or usage. Subscribing before inspection does not make those reads atomic.
Applying an older lifecycle event over a newer snapshot can regress a view.

Add a per-agent lifecycle revision. Increment it under the existing lifecycle lock
only when the state changes; capture state and revision together for snapshots,
control acknowledgments, and state events. Initial creation must carry the real
initial state/revision, not a separately reconstructed value. A client applies
only newer lifecycle revisions and ignores repeated ones. Missing intermediate
states are not reconstructed from a snapshot.

Keep history revisions: they identify exact append-only conversation prefixes for
token counting and model-call attribution, including when counting runs after the
agent has advanced. They are not lifecycle versions. Usage call numbers and
work/plan revisions likewise order only their respective data. For any remaining
unversioned fields, events invalidate a projection and clients refresh snapshots,
rejecting stale out-of-order refresh responses. An inspection still comprises
independent snapshots; an atomic global snapshot with an event watermark requires
a separate synchronization design. Pausing agents alone is not that design.

### P1: sharing stateful tools across sessions breaks identity isolation

Each controller starts its own agent ID counter. The
[web snapshot cache](tool/open_url.go) scopes cursors to an actor ID and URL,
without a session ID. Sharing one `Web` instance across sessions would let a
matching local actor use another session's cursor if it obtained that cursor.
Similar ownership mistakes could let one session close another's resources.

Factories create stateful tool runtimes per session by default; roles within that
session may share them. Agents receive handles bound to their runtime identity and
sender; model arguments cannot override that binding. The current `tool.Call`
already distinguishes runtime-supplied identity from arguments: preserve that
contract rather than require session IDs in every tool invocation. Shared browser
or file coordination belongs to the session; stopping a sibling agent does not
close it. Borrowed providers may be shared when their documented
concurrency and state contracts permit it. Stateful cross-session sharing requires
explicit session namespacing. A session workspace is a working directory, not a
sandbox: parallel evals need distinct workspace fixtures or intentional sharing.

### P1: command authority and retry semantics are underspecified

The internal workflow presently combines domain operations with `tool.Call` and
`tool.Result`, and exposes its store. The store accepts actor IDs, not authenticated
principals. Moving these callbacks verbatim into a public API would expose bypasses
and leave HTTP clients able to mistake request cancellation for rollback.

Extract typed application operations used by both model tools and trusted host
methods. Keep actor validation, root-only rules, provisioning, compensation,
revision checks, and dispatch in those shared operations. Transport authentication
chooses an authorized host capability; an arbitrary body field does not grant one.
Make ordinary user messaging distinct from trusted actor-attributed dispatch.
This preserves existing authorization rules; it does not introduce a general
permissions framework. Session close/disposal are owner operations and must not be
exposed as blocking model tools that could wait for their own agent to exit.

Session admission closes atomically against all mutation entry points. An admitted
mutation may commit even if its caller times out. Return stable resource/receipt
IDs and errors suitable for inspection. Existing work revisions fence updates but
do not deduplicate creation or message sends. Do not automatically retry mutations;
design idempotency keys before offering retry-safe HTTP creation. No session-wide
lock may be held across providers, tools, or shutdown waits.

### P2: the proposed event envelope needs ownership and encoding contracts

[Existing events](conversation/events.go) contain nested mutable payloads and a
Go `error` value. The closed `conversation.Event` interface cannot be extended by
implementing its unexported method in `harness`. An arbitrary interface payload
also lacks a stable JSON discriminator.

Define an API-owned event family with exhaustive conversion from conversation
and workflow events. Preserve tool-batch context boundaries, usage observations,
receipt milestones, and synthesized workflow failures, not just display messages.
Use explicit event kinds and structured error code/message data at the wire
boundary. Clone mutable content on publication and on return to each reader;
the reader must never obtain aliases into the log or another reader's payload.
Do not hold the event-log lock while deep-copying large payloads for a reader.

Scope sequences to a session identity; use agent identity plus tool/model call
identity for correlation. Sequence order is publication order, not an atomic order
of all state mutations. Capture the log before root creation so startup is visible.
Put common event/storage contracts below `harness` in the dependency graph, so
storage implementations and harness assembly can share them without import cycles.

### P2: complete eval evidence is broader than retained UI events

Current events and transcripts do not constitute an exact model-request archive.
[The recording proposal](RECORDING_DESIGN.md) is explicitly unimplemented and
tool-focused; it does not record all workflow events or full image data.

Treat observation replay, durable recording, and execution replay as different
features. An eval requiring a complete trace must verify its starting cursor,
detect retention gaps or missing payloads, and await finalization. Cursor retention
cannot guarantee lossless evidence under arbitrary load. Durable recording needs
an explicit failure/backpressure policy; being an ordinary observer is insufficient.
Record effective prompts, model settings, tool definitions, and artifact content
or resolvable references before claiming reproducible trial inputs.
Complete trial evidence also needs the harness version, initial workspace inputs
and resulting changes, actual provider requests/responses/errors, tool inputs and
outputs, and observed external data. Capture failures before responses enter agent
history as well. Logical provider requests and backend-translated wire requests
are different capture levels; declare which is recorded. Reproducible inputs do
not guarantee deterministic model outputs or unchanged external services.

Idle state, a reply, delivery consumption, accepted work, session closure, and an
eval pass are distinct facts. The runner selects a trial completion policy and
grades the result; closure by itself never means the task succeeded.

### P2: configuration sketches promise capabilities we do not yet implement

`Limits`, registries, workspace policy, and artifact retrieval were placeholders.
Freezing them now would either introduce unnecessary extension systems or expose
settings that the runtime cannot enforce. Automatic context counting is also
currently scheduled by the [TUI](internal/tui/tokens.go), so attachment affects
provider traffic even though it does not alter the agent's history or usage.

First extract existing settings, exact role tool order, presets, and injectable
provider/tool construction. Keep effective config immutable and inspectable, with
credentials excluded. Validate unsupported settings instead of ignoring them.
Promote automatic counting to an explicit harness telemetry policy with bounded
concurrency and deduplication by agent/history revision. On-demand token counting
is an active read that may perform I/O, not passive observation.

Add each new budget only with its enforcement contract, including how concurrent
agents reserve capacity and whether in-flight output can exceed the budget. Defer
generic registries, resumable persistence, and a separate run object.

## Corrected observation contract

The following are semantic contracts for the proposed API, not Go declarations.

| Operation | Contract |
|---|---|
| `Events(after, limit)` | Return a finite retained page, its last sequence, retained bounds, and closed state. An empty page preserves the requested cursor. Validate the page limit. |
| `Subscribe(after)` | Consume retained entries after an exclusive cursor, then wait for new entries without a history/live handoff gap. |
| `Subscription.Next(ctx)` | One reader per subscription. Cancellation ends this wait without advancing its cursor. Each successful call returns an owned event snapshot. |
| `Subscription.Close()` | Idempotently detach this observer and wake a pending read. Other observers and session execution continue. |
| Session log seal | An attached reader drains retained entries, then receives `io.EOF` after a successful seal. Retained paging remains available until explicit disposal. Storage/publication failure ends a reader with an error, not clean EOF. |

The subscription's creation context, if retained by the API, controls only its
attachment lifetime. It must never be the execution or finalization context.

- Sequence numbers start at one and are never reused within a session.
- `after = 0` requests the beginning, not "whatever happens to remain."
- For retained earliest sequence `E`, `after < E - 1` reports an expired cursor.
- `after == latest` waits for new data, or yields EOF after closure.
- `after > latest` is an invalid cursor, not an indefinite wait.
- Tail-only attachment is explicit, through a tail option or a captured latest
  cursor; it must not silently replace a request for complete history.
- Gaps are errors even after closure. EOF must not hide missing final events.
- Limit retained entry count and accounted payload bytes. Neither measure is a
  promise about total Go heap usage or transient copying allocations.
- A single oversized payload needs a defined policy: publish a small explicit
  omission record with its original kind and identifiers, and mark trace
  completeness false. Never silently drop or truncate it. Artifact references
  may replace omissions once artifact storage and lifetime are implemented.
- Preserve terminal status and trace-completeness metadata outside the evictable
  ring as part of session inspection. This does not reconstruct evicted events.
- Validate retention settings to fit required small control/omission records.
  Eviction changes the retained bounds; omitted payloads and publication failure
  change capture completeness. Track those separately, since a collector may have
  already persisted entries subsequently evicted from the live store.

## Event storage seam

Introduce the smallest storage interface needed by the real in-memory backend.
The following is a sketch; event/query/outcome types are shared contracts, not
untyped maps. The store belongs to one session and has one publisher. Observers
cannot obtain its mutation methods.

```go
type EventStore interface {
    Append(ctx context.Context, data EventData) (Event, error)
    Read(ctx context.Context, query EventQuery) (EventPage, error)
    Seal(ctx context.Context, outcome ShutdownOutcome) error
    Close(ctx context.Context) error
}
```

- `Append` assigns the next sequence with the append. A successful return means
  the event is readable. Durability is a separate documented backend setting;
  readable does not automatically mean synced to disk.
- `Read` returns a consistent retained page and its earliest/latest committed
  sequences and sealed state. No empty page/cursor response may skip unread data.
  A read concurrent with eviction may return owned copies it already captured.
- `Seal` appends the terminal record and seals publication as one logical
  operation, after all previous accepted publications. Repeating the same seal
  returns the established outcome; a different outcome is rejected. Appends after
  seal fail. The terminal status remains available even if its event is evicted.
- `Close` releases backing resources. It is storage disposal, not session execution
  shutdown. It matches `Resource`, is idempotent, and happens after in-flight reads
  finish or cancel. Timeout preserves ownership; callers can finish cleanup later.
- The in-memory implementation uses a retained buffer with entry and accounted
  byte limits. Future SQLite/JSONL implementations must meet the same cursor,
  ownership, error, and sealing contracts; they need not use identical retention
  strategies. Persist a schema version and session identity in durable records.
  Incomplete/corrupt files must not be reopened as successfully sealed sessions.
- Event persistence provides history access, not recovery of a running agent,
  workspace, pending dispatch, or browser. Resuming execution remains separate.

Subscriptions are implemented above storage. A publisher serializes writes;
successful append, seal, or publication failure wakes a shared notification hub.
Readers capture a hub generation **before** reading the store, then wait on that
generation if no event is available. Recheck in a loop. This avoids a lost wakeup
between reading and waiting without holding a hub mutex across disk I/O. There
is no reader goroutine or queue per observer. Detach, disposal, and publication
failure also wake blocked reads. Live external writers to the same store are out
of scope; all mutations pass through this session's publisher.

Moving writes off execution callbacks introduces a pending publication queue;
bound both its entry count and accounted bytes. It replaces the host relay rather
than sitting after it. Submission copies payload ownership before returning and
never waits for disk. A full queue or failed/ambiguous append latches a publication
failure and capture-incomplete state outside the store. Stop accepting further
publication and wake readers: no silent drops, clean EOF, or automatic retry that
could duplicate an append. Core dispatch may continue independently; an eval
requiring complete evidence must reject that trial. Strictly aborting execution
on recording failure can be an explicit later policy.

`Flush` on the publisher is an ordered barrier: it waits for earlier admitted
publications or returns the latched error. It is not an empty-queue poll and does
not establish filesystem durability. The memory backend has no durable guarantee;
future durable backends must define what successful seal guarantees and how flush
or sync failures are surfaced. A disk-full failure can prevent a terminal record
from being written: session inspection and `Close` still report that failure.
After a crash, absence of a valid durable seal means incomplete finalization.

Storage capabilities and capture coverage are independent. Adding a disk backend
does not add missing model request/response or artifact instrumentation. A durable
event store can hold the expanded execution record when those producers exist;
avoid a competing recorder assigning another canonical event sequence.

## Managed resources and session-owned provider transport

Use an explicit lifetime contract for owned resources:

```go
type Resource interface {
    Close(ctx context.Context) error
}
```

Repeated close calls are safe; successful close confirms cleanup. A deadline does
not transfer ownership or imply completion. Registration explicitly identifies an
owner; implementing the interface never makes a borrowed dependency owned. Tools
and providers implement this additional contract only when they own something
requiring cleanup. Execution interfaces stay separate from lifetime interfaces.
Failed cleanup is retryable by calling `Close` again on the unfinished resource;
the implementation must retain enough state to do so. Cleanup already confirmed
successful is not repeated. This is lifecycle retry, not retry of a tool action,
model request, or ambiguous storage append.

Ownership is hierarchical: the session owns `tool.Web`, which owns its WebKit
worker. Do not also register that worker with the session. Controller/workflow
lifecycle ordering remains explicit in the coordinator rather than an arbitrary
iteration over resources. Per-invocation shell processes, PDF temporary files, and
isolated page-browser sessions remain owned and cleaned up by their invocation.

**Dedicated provider HTTP transport is part of session assembly.** Today the CLI
creates an `http.Client` without a `Transport`, so provider requests use the shared
default transport. The new harness creates a private `http.Transport` per session
and injects clients backed by it into its HTTP providers. A new client alone is
insufficient to isolate the connection pool. Configure a fresh transport, or clone
an explicitly supplied transport configuration without sharing live connections;
do not mutate or close process-global defaults.

```text
Session owns provider transport resource
  -> session-local connection pool
     -> provider HTTP client(s)
        -> root / implementor / auditor providers
        -> token counting
```

Provider adapters borrow these clients and do not close the shared session pool.
Client timeout settings may differ while clients share that transport. Preserve
the current generation timeout and shorter telemetry deadline; transport ownership
does not add generation retries, alter presets, or change provider error handling.
The browser subprocesses use their own networking and are separate resources.

The transport resource needs an admission/lifetime wrapper, not just a renamed
`CloseIdleConnections`: that method neither cancels active requests nor prevents
future requests. Seal admission and cancel requests when execution closes, track
each request through response-body EOF/close (not merely response headers), and
join users before closing the pool's idle connections. Reject new requests after
closure with a stable error. A closing caller can time out while the owner keeps
track of unfinished cleanup. Provider and telemetry operations participate in the
session's resource-use tracking; do not count nested work as new host commands.
Response-body cleanup and request deregistration must happen exactly once on all
paths: response consumed, explicitly closed, round-trip failure, or cancellation.
An EOF path still releases the underlying body's resources. Link session and
request cancellation without replacing the caller's deadline/context values;
remove cancellation hooks at completion. Wrappers must preserve normal HTTP
request/response semantics and must not eagerly buffer entire bodies for logging.

Built-in providers constructed by the harness always use the session transport.
An explicitly injected custom provider remains a borrowed collaborator unless its
factory transfers resource ownership. A custom provider's hidden HTTP connections
cannot be claimed as session-owned. Factories for test transports likewise state
ownership explicitly; a shared round tripper is not implicitly disposed.

## Session diagnostics

Session diagnostics share event publication, correlation, and storage. Capture
tool start/finish records, arguments, result content, timestamps, structured errors,
and harness-generated invocation IDs. Include successful operations so failures
can be explained by prior activity. Capture diagnostic snapshots at the operation
site, especially the exact file text searched by a failed edit; a later read is
not equivalent evidence. Keep host diagnostic detail separate from model content.

Measurements and diagnostic logs are typed records alongside execution events.
Record errors once at the operation boundary, retaining structured lower-level
causes. Expected tool outcomes such as a nonzero shell exit remain distinct from
transport/runtime failure. Detailed capture is configurable and omissions are
explicit. Recorder failure uses session status and a separate host diagnostic sink
so it does not recursively depend on the recorder. The capture examples in
[RECORDING_DESIGN.md](RECORDING_DESIGN.md) remain useful, but session assembly and
the canonical event publisher supersede that document's CLI-owned recorder wiring.

The first diagnostics delivery includes an explicitly configured JSONL event-store
backend, after the memory backend contract is working. Persist records as execution
proceeds; a final export alone does not address debugging an interrupted process.
Memory remains the default when durable recording is not configured. Each JSONL
session creates a new record file exclusively; never overwrite an existing trace
or append a new session to an old one. Surface creation/write/flush/sync failures.
Successful durable seal requires flushing and synchronizing the record before
reporting success. An interrupted record without a valid seal remains explicitly
incomplete. SQLite and runtime recovery remain later work.

JSONL uses the same sequence, payload schema, and store contract as memory, with
paged reads and bounded transient buffers. It must not reconstruct the full file
in RAM to answer a page. A simple scan or an index with an explicit memory bound
is acceptable initially. A configured record-size limit fails recording visibly
rather than deleting evidence or silently rotating it. Exact capacity defaults
are configuration/tuning decisions, tested at tiny limits as well as defaults.

Track three independent dimensions in inspection and exports:

| Dimension | Meaning |
|---|---|
| Coverage | Which sources/details are enabled and implemented: tool activity, edit snapshots, logical provider calls, wire payloads, artifacts, etc. |
| Capture health | Whether admitted data was lost, omitted, or failed to persist; include the failure reason and last confirmed committed sequence. |
| Retained range | Which committed sequences this store can still return. Eviction is not proof that an already-running external collector lost them. |

A healthy tool-only recording is not a complete model/eval archive. Diagnostic
severity filtering must not remove canonical tool/error/lifecycle records. Full
edit snapshots and successful tool arguments/results address the initial debugging
use case; wire bodies and artifact storage are additional declared coverage, not
implied by enabling recording. Derive durations/counts from execution records where
possible instead of emitting duplicate records for each logging destination.

## Session shutdown and disposal

The previous code sketch described the intended dependencies, but its agent-only
join missed automatic telemetry and explicit token-count requests. Those can still
use a provider after an agent exits. Track all session-owned resource users.

```text
Open
  -> Closing: seal admission and stop initiating dispatch/telemetry
  -> cancel execution and active resource I/O
  -> join admitted commands, agents, and resource users
  -> drain controller events and final workflow outcomes; stop dispatcher
  -> close execution resources; admit their cleanup outcomes to publication
  -> flush publisher; seal event store, or report publication/seal failure
Closed (execution finalized; history readable)
  -> Dispose: detach observers, cancel/join storage reads, close event store
Disposed
```

The transition out of Open happens once, and one finalization attempt runs at a
time. Concurrent close callers join that attempt. A caller's `Close(ctx)` timeout
only stops that caller's wait; the attempt continues. If an owned resource returns
a cleanup failure, retain Closing plus the unfinished ownership and report the
error. Do not seal the terminal record or spin on automatic cleanup retries. A
subsequent Close starts another attempt for unfinished phases only. Once all users
and resources have finished, sealing produces a stable final outcome for repeated
callers. Execution may have failed even when its resources close successfully.

Publication/seal failure is different from an active execution resource: once
execution cleanup has completed and the publisher worker has exited, the session
may be Closed with capture failure and no valid durable seal. Do not automatically
repeat an ambiguous append/seal. Report the failure outside the store; disposal
can still close its backing handle. Parent context cancellation initiates the
same coordinator rather than directly killing dispatch and collection.
Construction failure rolls back resources already acquired in reverse dependency
order and reports cleanup failures. If rollback cannot finish, return an explicit
cleanup handle with the construction error so the owner can retry disposal;
never leave an unreachable live resource or return a usable partially built session.

Admission and transition to closing use one synchronization boundary. Count each
top-level host operation or model invocation once; its internal workflow steps,
compensation, and outcome publication do not recursively reacquire admission.
Otherwise, sealing between provisioning and ledger mutation could reject the
operation's own completion/cleanup. Required internal receipt processing remains
available in closing mode but cannot start new assignments. Never wait on a
command, agent, subscriber, or resource while holding its admission/lifecycle lock.
Owner `Close`/`Dispose` calls are lifecycle coordination, not tracked execution
commands; otherwise the finalizer could wait for the very caller awaiting it.

Passive in-memory inspection and history paging remain available after close.
Operations requiring a provider or another execution resource reject admission
once closing begins, even if called an inspection method. Shutdown does not wait
for observers to consume retained events. It waits for admitted publication to be
processed, which can finish with an explicitly recorded error.

Expose an owner-only `Dispose(ctx)` operation distinct from `Close(ctx)`: it starts
or awaits execution close first, then detaches observers and releases storage.
Timing out does not force-close resources still in use. A local CLI normally
disposes on process exit; an eval owner closes, exports/grades, then disposes.
HTTP session retention/expiry is manager policy added with that adapter, not a
timer hidden inside `Session`. Borrowed dependencies are never disposed by it.
Disposal must seal storage-read admission before cancelling/joining admitted
reads, so a concurrent new page request cannot race handle closure. Preserve
in-memory terminal status for inspection after disposal, but reject history I/O.

Session finalization does not change unresolved work into successful work. The
terminal outcome reports execution/cleanup errors and capture status separately.
If cleanup cannot confirm resources are closed, retain/report the outstanding
ownership instead of claiming successful disposal.

## Implementation sequence and acceptance checks

Each stage must work before introducing the next public surface. Preserve the
existing CLI defaults and behavior unless a correction is explicitly documented.

1. **Shared workflow operations — implemented.** Typed methods in
   [operations.go](internal/workflow/operations.go) perform assignment,
   reassignment, plan/work mutations, and authorized reads. Model tool adapters
   delegate to them. `work.AssignmentRequest` and `work.Inspection` are shared
   domain contracts; the existing tool request name remains a compatible alias.
   [Operation tests](internal/workflow/operations_test.go) compare the complete
   audit/repair cycle through direct and tool calls, including replacement/cancel,
   root-only restrictions, stale revisions, cancellation, and provisioning rollback.
   Root-only plan editing is enforced at the operation boundary as well as by tool
   selection. Empty replacement assignees serialize as omitted, matching automatic
   provisioning. Stage 3 adds session-wide admission/close coordination.
2. **Public session assembly — implemented.** Move role prompts, provider resolution, local-tool
   assembly, management/inspection adapters, and cleanup into `harness`. Change
   the CLI to use it. Check exact effective role configurations, partial startup
   cleanup, distinct session resources, and a headless scripted audit/repair cycle.
   Include the `Resource` contract and a private provider transport per session.
   Check connection reuse within a session, independent pools across sessions,
   cancellation/body cleanup, rejection after close, repeated close, and that
   closing one session leaves another session and borrowed transports usable.
   `Session.NextEvent` is a transitional single-reader adapter for the existing TUI;
   stage 4 adds independent subscriptions. The controller/store remain
   private, and external-package tests exercise construction and audited work.
3. **Lifecycle finalization — implemented.** Add the admission gate, separate cancellation and
   draining, and stable closing outcome. Check send/assignment racing close,
   timeout followed by another close, parent cancellation, cleanup failure,
   pending receipts, nested workflow compensation, resource I/O racing cleanup,
   and final event delivery. Do not make closure cancel an observer context that
   is needed to read the tail. A blocked dependency never produces false closure.
4. **Independent observation and lifecycle versions — implemented.** Implement `EventStore`
   with bounded memory retention and a backend contract test suite. Replace the
   host relay with bounded publication and adapt the TUI to a subscription. Check
   two readers, no reader, detach while work runs, reconnect without duplicates,
   expired/future cursors, oversized events, mutation isolation, storage failure,
   publisher saturation, flush ordering, repeat seal, EOF, disposal, and wakeups
   across read/append races. Verify synthesized workflow/startup events and add
   lifecycle revisions to state snapshots/events; test stale events and snapshots
   racing controls without confusing history revisions with state revisions.
5. **Persistent diagnostics — implemented.** Implement JSONL against the event-store contract,
   with tool invocation correlation and edit-failure snapshots. Check successful
   and failed calls, wrapped errors, exact searched text despite a subsequent edit,
   original model-visible errors, exclusive file creation, bounded paging, write/
   sync failure, read-only inspection of an interrupted trace, and durable sealing.
   Capture coverage, health, and retained range must remain distinguishable.
6. **Headless eval readiness — implemented.** Centralize automatic telemetry policy, expose
   completeness and effective-config inspection, and document trial completion.
   Check attachment does not change configured provider/tool calls, and use an
   external-package Go test to prove no internal imports are needed. Compare
   domain results and causal events, not timing-dependent total event order.
7. **HTTP adapter — implemented.** Add wire DTOs, error mappings, authorized host capabilities,
   event encoding, session lookup, and deliberate retry/idempotency behavior over
   the established API. Contract-test it against direct Go calls with scripted
   dependencies. Disconnect must only detach the request/subscription.

Run focused tests during extraction and `go test -race ./...` at the relevant
integration boundaries. Existing opt-in live browser/model tests are separate from
scripted parity tests; neither is a replacement for the other.

Before calling the foundation complete, also test repeated failed/successful
cleanup, an active transport response body during close, and disposal racing new
history reads. A session with publication failure must remain controllable and
inspectable even though its subscription reports failure; adapters display that
failure once and offer explicit snapshot refresh rather than retrying the broken
subscription forever.

## Implementation decision boundary

The architecture, ownership, overload behavior, shutdown phases, and first
diagnostic delivery are settled for implementation. Final method names, small
internal interfaces, numeric defaults, and indexing mechanics can be chosen in
the relevant vertical slice without changing these contracts. Constructor errors
need an explicit cleanup-handle shape; it can be a typed error implementing a
documented cleanup interface. Successful construction still returns one fully
assembled session with an idle root.

The first version deliberately does not promise bounded total runtime memory,
exact execution replay, automatic mutation retries, a general permission system,
an atomic global snapshot, or full eval evidence coverage. Each would require a
separate behavior contract. HTTP details and SQLite implementation are later
milestones and do not block the Go session, memory store, or JSONL diagnostics.

## Foundation scope

The first three slices establish shared typed workflow operations, their tool
adapters, public `harness.Session` assembly with owned resources and transport,
and coordinated shutdown. `Close(ctx)` starts independent finalization and only
bounds the caller's wait. Commands reject admission while closing; inspections
and the final event tail remain available. Cleanup failure leaves `State()` at
`Closing`, and the next close retries only unfinished resources.
The fourth slice adds public `eventlog.Store`, a bounded memory implementation,
ordered publication, independent cursor subscriptions, and explicit disposal.
`Events`, `Subscribe`, `Capture`, and `FlushEvents` expose observation without
exposing store writes. The default retention is 4,096 entries / 16 MiB and the
publication queue is 1,024 entries / 8 MiB. Inject storage with
`Dependencies.EventStore(sessionID)`. Call `Dispose` to release storage after
inspection; `Close` retains it. Capture failure is a stable close error, but
execution can still reach `Closed` after its resources have been released.
The TUI uses a subscription and can refresh snapshots after observation failure.
Agent state snapshots/events carry a lifecycle revision independent of history.
The fifth slice adds exclusive JSONL recording (`EventConfig.JSONLPath` or CLI
`-record`), read-only inspection of interrupted traces, typed host logs via
`Session.Log`, and an independent `Dependencies.CaptureFailure` sink. The sink
must return promptly and must not wait for session finalization. Tool records
carry runtime invocation IDs, arguments (including malformed JSON), result
content, error text, timestamps, and host-only edit diagnostics. File edit failures
preserve the exact searched text, resolved/requested paths, old/new strings, and
SHA-256 from the operation itself; model-visible errors remain unchanged.
JSONL reads scan with bounded buffers and no growing offset index. Successful
seal syncs the file; append/flush alone do not establish durability. Reopening
verifies record structure and ordering, not historical success of an fsync call.
Full model request/response and external artifact capture remain outside current
coverage. The sixth slice moves automatic context counting into a session-owned worker
pool with configurable timeout, bounded concurrency/queue, and per-agent history
revision deduplication. The TUI observes measurement records; `/agents` remains
an explicit on-demand measurement request. `Configuration()` resolves built-in
provider presets and records role prompts/tool order, while injected providers
are marked opaque. `Inspect()` reports state, configuration, capture health and
coverage without claiming a global atomic snapshot. External-package tests prove
that observer attachment does not change provider traffic. The seventh slice adds `harness/httpapi.Service`, explicit host authorization,
HTTP DTOs/error codes, managed session lookup, finite pages, and NDJSON streams.
CLI `-listen` serves on loopback using `STRAP_API_TOKEN`. Disconnect only detaches
observation; session factories use service lifetime. Dynamic agent collaborators
are selected by host profiles. Mutation requests are not retried; unsupported
idempotency keys are rejected. Contract tests run the full audit/repair cycle
through direct Go and HTTP calls and verify real stream reconnection. See
[the HTTP API](harness/httpapi/README.md).

## Final integration audit

Terminal outcomes now persist the initiating reason, execution error summary,
cleanup attempts/errors, and capture status outside retained event storage.
Failed cleanup emits a diagnostic and keeps `Closing`; a successful retry seals
with the final cleanup outcome. `Dispose` advances to `Disposed` while keeping
terminal inspection available. Resolved configuration is also recorded as a
`session_configured` event. Oversized tool records keep runtime invocation
correlation in their omission record. If the terminal outcome itself cannot fit,
sealing reports capture failure rather than replacing the terminal record with
an apparently successful seal.

Canonical event serialization lives in `harness/eventcodec`, keeping storage/wire
encoding out of agent execution and conversation routing.
