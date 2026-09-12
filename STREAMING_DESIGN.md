# Streaming and recoverable session views

Status: revised design, not implemented. Audited 2026-09-12 against repository
state `094885f`. This is the proposed successor to the observation contracts in
[HARNESS_DESIGN.md](HARNESS_DESIGN.md), recorded in
[ADR-002](docs/architecture/ADR-002-recoverable-session-log.md).

## Decision

One recoverable session log, one subscription contract, and projections built
from that log. A view can attach at any time and reconstruct accepted messages,
transcripts, work, agent state, tool activity, and generated output, including
generation in progress. Disconnecting a client only detaches its subscription.
Execution does not depend on a view being present or consuming events.

The log is authoritative for reconstructing views. Agent loops, routing, and the
work ledger still execute commands; replay must never rerun a provider, tool,
delivery, or work assignment. This change does not introduce execution recovery.

```text
providers / tools / agents / routing / work
                    |
           required session publication
                    |
          one ordered, recoverable log
                    |
       replay existing records, then follow
          /             |              \
     TUI projection  HTTP clients  SDK / eval projection
```

## Subscription contract

Proposed Go shape; these signatures replace, rather than describe, today's API:

```go
type Cursor struct {
    Session  string
    Sequence uint64
}

type SubscribeOptions struct {
    After Cursor // Zero value starts at the beginning of this session.
}

func (s *Session) Subscribe(ctx context.Context, opts SubscribeOptions) (*Subscription, error)
func (s *Subscription) Next(ctx context.Context) (Record, error)
func (s *Subscription) Close()
```

- A fresh view starts at zero. An existing projection resumes after its last
  successfully applied cursor. Persist the projection and cursor together; a
  cursor alone is not enough to reconstruct a fresh view.
- Records have a session identity, strictly increasing contiguous sequence,
  schema version, kind, timestamp, typed payload, and correlation identities.
  The cursor identifies publication order, not wall-clock or global execution order.
- Each subscription has one reader and returns records in sequence. Different
  agents may interleave. Duplicate records after a reconnect are safe to ignore
  by cursor; unknown required schemas/kinds fail explicitly rather than silently
  producing an incomplete projection.
- Future and wrong-session cursors are errors. There is no normal expired cursor
  while the recoverable session is retained: committed records are not evicted.
- Existing records and live records use exactly the same path. No snapshot call,
  snapshot token, lease, or special bootstrap reducer is required.
- Reads are paginated with record and byte budgets. A subscription retains only
  a bounded read window and a cursor, never a private copy of the complete log.
- Canceling a pending read does not advance its cursor. The attachment context or
  Close detaches the reader without closing or canceling the session.
- A closed session can still be replayed; its terminal record precedes EOF.
  Read/storage failure is an error, not EOF or a successful terminal outcome.
  Disposal revokes reads. No reconnect or read operation retries a mutation.

Optional catch-up progress reports the log head captured when attachment began.
Reaching it means the initial retained prefix has been applied, not that agents
are idle. Such progress is subscription metadata, not another session event.

## Storage and ordering

Evolve the existing store boundary rather than adding a snapshot service. Its
essential capabilities are ordered append, finite reads with a head watermark,
waiting for a sequence advance or terminal/failure state, sealing, and disposal.
The session exposes subscriptions; backends supply these storage guarantees.

Append returns only after a complete immutable record is readable and assigned
its sequence. Uncommitted writes are invisible. A read-then-wait loop must not
miss an append between those operations: waiting checks the current head and
terminal/failure state under the same synchronization used to register a waiter.
Wakeups are hints; readers recheck the store predicate. Keep backend I/O and
mutable buffer ownership private, with no aliases exposed to readers.

Required records remain available from sequence 1 until explicit disposal. The
current Memory eviction and Fit omission records cannot implement this contract.
Their omissions are explicit today, but still insufficient to reconstruct content.
The memory variant retains the full accepted log and consequently grows. A disk
variant retains history while bounding working buffers. Configuration exposes
storage/record quotas and queue capacity. Queue capacity applies backpressure;
exhausted storage quotas or an unrepresentable record fail explicitly, never
evicting accepted content or successfully writing an omission in its place.
Disk-backed logging alone does not bound all runtime memory: agent model context,
projection indexes, and TUI caches need their own paging/budget policies. Readers
must not accumulate all content into strings merely to follow the log.

Large text, images, and other required payloads use bounded content-chunk records
in the same log, followed by a referencing domain record. References identify
immutable content, byte length, and integrity information. Readers can page the
content through the store. The referencing record is accepted only after its
content is complete. Interrupted content has no successful referencing record.
A workspace filename or expiring backend URL is not a recoverable content value.

The existing JSONL implementation needs an efficient sequential read path: it
currently scans from the start for each page, which would make long replay and
one-event subscription reads repeatedly scan the same prefix. Use bounded read
windows or disk-backed indexing; do not substitute a growing per-reader index.

Readable append and crash durability are different guarantees. Backends must
declare their sync behavior. Client reconnection assumes the owning harness is
running and its storage is healthy. Reopening an archive does not restart agents,
recover external side effects, or promise records beyond the durable prefix.

## Required publication and failure

Reliable view reconstruction makes session publication an execution dependency.
It is independent of clients, but cannot be an optional, lossy recorder callback.
Optional exporters may consume the log and fail independently; they are not a
second authority for session state.

Use one bounded publication path with backpressure to producers, including the
controller ingress. Capacity pressure waits with cancellation outside lifecycle,
routing, and work locks. Slow client consumption does not hold that path or gate
execution. Never hold a store writer lock while sending data to a client.
Slow required storage can slow generation; a configured deadline, quota, or write
failure causes an explicit session failure and coordinated execution cancellation.
Do not add another unbounded relay upstream of a bounded store.

Runtime transitions need explicit publication completion, not fire-and-forget
notification. A successful host receipt must not precede acceptance of the records
that describe its result. For one entity, preserve transition order and publish
identity before dependent activity. Reserve capacity or restructure transitions
before replacing callbacks that currently execute under runtime locks. Never wait
for the publication worker while holding a lock it needs to complete that write.
The writer must be independent of workflow dispatch: a workflow operation cannot
wait for its own event-processing loop to persist its result. The writer does not
invoke execution callbacks. Preserve causal publication order, such as message
acceptance before its consumed receipt, even when producers run concurrently.

Execution cancellation stops new production; already admitted publication and
terminal output flushing use the separate drain lifetime. Do not use the canceled
provider context to discard buffered text during ordinary stop. Storage deadlines
still bound a failed drain and produce an explicit recovery failure.

This is not an atomic transaction across external effects, domain state, and
storage. A tool may have changed a file when recording fails. Do not claim rollback
or retry the tool/ambiguous append. Latch the failure, stop new execution, preserve
the readable accepted prefix, and report uncertainty outside the failed log.
Close still joins resource users and releases execution resources. A storage
failure may prevent a terminal record; a missing terminal record never means
successful completion. Close can finish with an explicit recovery failure.

## Records and correlation

Correlation and a linear sequence serve different purposes. Define shared
identities below agent/message dependencies, in `identity`, to avoid import cycles.
Use session + agent + model-call identity for output, message identity for routing,
and runtime invocation identity for tools. Provider tool-call IDs alone are not
session-unique. Allocate model-call identity before generation begins, and reuse
it in output and usage observations without incrementing completed-call accounting
until the call returns. History and lifecycle revisions keep their current meanings.

| Record family | State reconstructable from the log |
| --- | --- |
| Session configuration/lifecycle | Effective configuration, startup, closing, outcome |
| Agent lifecycle | Identity, parent, ordered state transitions |
| Messages and receipts | Accepted content, routing, queued/consumed/undelivered state |
| History appends | Exact committed model transcript entries and per-agent positions |
| Model outputs | Active text, final text, failed/canceled partial text, call identity |
| Tool activity | Invocation identity, arguments, result, timing, errors, captured diagnostics |
| Work and plans | Accepted revisions, assignments, submissions, audits, progress |
| Usage and measurements | Per-call accounting and revision-bound token measurements |

Each accepted view-state change must have a sufficient record; current domain
events are not yet sufficient, especially for transcript appends and failed model
output. Preserve original message envelopes and complete content. Correlation is
host metadata and must not leak into model-visible input through envelope JSON.
Historical records carry their original timestamps so replay does not reset timers.

Model transcript history and the visible session timeline are separate projections
of these records. A routed reply, its output completion, and its history append
refer to the same logical output; they do not create three visible messages.
An output can complete before delivery or tool execution finishes.

## Provider streaming

Use one provider execution contract for streaming and non-streaming providers:

```go
type Delta struct { Text string }

type Observer interface {
    OnDelta(Delta) error
}

type Provider interface {
    Submit(context.Context, Request, Observer) (Response, error)
}
```

Callbacks are serial within one submission and finish before Submit returns.
They may wait for bounded required publication and return an error. On callback
failure the provider stops reading, closes its response body, and returns failure;
it must not treat the generated response as accepted. Callback implementations
must not reenter session mutations or Close. External observers use subscriptions.
Different submissions may invoke callbacks concurrently. A nil observer changes
observation only, not generation settings or model execution policy.

Text deltas are nonempty valid UTF-8 and form an append-only prefix of the final
response. On success the agent fills any remaining suffix from the validated
response, which also supports providers emitting no deltas. A conflicting final
response is a protocol failure. Failure preserves recorded deltas without adding
them to model history. Usage can accompany failure and is accounted once.

Adapters assemble tool arguments internally and validate them before returning
success. Premature EOF, malformed protocol, unsupported completion, and invalid
tool calls cannot dispatch tools. Bound parser, output, and tool-argument buffers;
exceeding limits fails explicitly. Consume the protocol's valid completion and
accounting records. Never silently retry a partially observed model call.

The agent coalesces small text deltas using a byte threshold and flush interval,
before entering controller publication. Delivery latency remains subject to storage
backpressure; coalescing does not promise a hard end-to-end deadline. Flush on
terminal paths. Any timer/worker is call-owned, canceled/joined before completion,
and participates in shutdown.
Coalescing never drops text; it changes chunk boundaries only. TUI redraw
throttling is independent of storage and provider execution.

## Output lifecycle and inspection

```text
output_started -> output_delta* -> output_finished
```

Start identifies the output and submitted history revision. Deltas identify the
output and the starting UTF-8 byte offset. Finish identifies completed, failed,
or canceled status, final text length/content reference, a structured error when
applicable, and the committed history position on success. Pending text is
published before finish. Exactly one terminal transition is produced under normal
execution and cancellation; storage/process failure can interrupt its recording.
Accept output_started before initiating the provider call. Completion references
an already recorded history append; tools cannot begin from an unrecorded
successful output. A text-free tool-call response is valid and need not create
an empty visible chat row.

Completed means validated and committed to assistant history. It does not mean
delivered, tools completed, or work accepted. Establish a local commit boundary
ordered against stop/cancellation: cancellation accepted before commitment wins;
after commitment it cannot retroactively mark that output canceled. Failed and
canceled text remains in session history but never enters model history.

Keep existing pause semantics: the current provider operation continues while
PauseRequested, then pauses at the execution boundary. Generation completion is
published before waiting at that boundary. Stop cancels active provider I/O.

An inspectable output is a projection of the accepted log prefix: output identity,
status, text length, committed content or pageable content reference, and applied
cursor. It can lag unaccepted provider bytes. Active-to-terminal transition keeps
the same identity and preserves partial text on failure. Inspectable projections
must not mix direct runtime reads with a claimed log cursor.
Paged output inspection selects one applied prefix and keeps its content length
and status fixed across those pages. It is an optional finite read, not a required
step in attachment. If a projection has not reached a requested minimum cursor,
wait with the caller's context or report lag explicitly; do not label older state
with the newer cursor.

For every supported prefix N, `Apply(records[1:N])` defines the view at N. Server
read models use that same reducer and expose their applied cursor. They are
derived caches, not execution controllers or a second copy of authoritative state.
A named Snapshot would mean a fixed projection at a cursor; no public Snapshot
interface or checkpoint implementation is required for initial subscription.

## Views, HTTP, and ownership

The TUI hydrates through the same reducer it uses for live updates. Accepted user
messages come from the log; optimistic input reconciles by receipt/message ID.
Completed output, commentary, and routed messages reconcile by output identity.
Preserve copy-mode freezing and throttle rendering without discarding source data.
Do not reconstruct state by invoking tools or reissuing historic commands.

Scroll position, selection, unsent drafts, and local display filtering remain view
preferences. They do not delete session history. CLI flags configure the host;
an attached view does not own session disposal. A standalone process may explicitly
close its session on exit; continued generation after that process exits requires
a separately running host. HTTP request/disconnect contexts only own attachments.

HTTP and Go expose the same ordered records and exclusive cursor semantics.
Authorize replay/content reads as well as live access. A complete view subscription
cannot silently filter required records. Envelope/payload schema evolution must
be versioned; existing diagnostic archives must not be advertised as complete
recoverable sessions. Existing HTTP clients ignoring unknown records are not
automatically compatible with the new reconstruction contract.

Close seals execution and drains required publications, then seals the log.
Shutdown does not wait for clients. Closed sessions remain recoverable until
owner-controlled disposal. Disposal cancels/joins reads and closes storage handles;
it does not silently delete a separately retained archive file.

## Revision audit and implementation gates

The following issues were found in auditing this revision and resolved in the
contracts above. These are requirements for implementation, not claims that the
existing code already meets them.

| Finding | Required correction and evidence |
| --- | --- |
| Cursor replay was still described as expiring | Full accepted log retention; reconnect from zero or any valid cursor after extensive generation |
| Snapshot handoff added unnecessary state/leases | One read/follow path; append/close races between empty read and wait never hang or skip |
| Old recorder acknowledges enqueue, evicts, and omits payloads | Required append acceptance, no lossy required records, bounded chunk storage; quota/write failures are explicit |
| Blocking callbacks could deadlock under existing runtime locks | Audit each producer/consumer lock dependency before wiring backpressure; saturate publication while stopping agents/closing session |
| Workflow could wait on its own publication consumer | Independent writer; test a workflow-originated mutation with full queues and concurrent close |
| Canceling the provider could discard its buffered text | Separate publication drain lifetime; stop during a partially filled coalescing buffer |
| Model history alone misses view content | Record inventory above; rebuild queued input, active tools, work, and failed partial output |
| Terminal output can duplicate messages or race cancellation | Stable correlation and commit ordering; completion/routing/pause/stop interleavings |
| Current TUI suppresses replayed user messages | Shared reducer; optimistic accepted input appears once in live and reconstructed views |
| JSONL replay can repeatedly scan the complete prefix | Sequential disk read path and bounded client buffers; long-log reconstruction check |
| Logging failure was assumed harmless to reconstruction | Fail affected execution; readable accepted prefix plus out-of-band health, with no fabricated successful seal |
| Durable recording was confused with process recovery | State sync guarantees explicitly; no automatic execution recovery or side-effect retry |

Implement in dependency order: recoverable storage/publication and record coverage;
shared projection/subscription contract; provider/output lifecycle and active
inspection; TUI/HTTP integration. Each stage must pass its contract tests before
being committed. Intermediate legacy diagnostic behavior must be labeled as such;
do not expose the full recovery guarantee until every required record is present.

The decisive acceptance test compares projections at the same cursor: uninterrupted
reader, detached/resumed reader, and fresh full-replay reader must agree. Exercise
multiple agents, UTF-8 chunks, no-delta providers, rejected/truncated streams,
tool argument fragmentation, cancellation around commitment, late usage, active
inspection, close/dispose, failed output, storage saturation, chunked large content,
and both memory and disk stores. Verify zero replay-driven provider/tool traffic.
