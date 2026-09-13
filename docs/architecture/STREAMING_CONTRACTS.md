# Recoverable session log: types and interfaces

Proposed trace-evidence extensions are specified separately in
[Session trace improvements: types and contracts](TRACE_CONTRACTS.md).
They retain this log/subscription architecture and are not yet implemented.

Implementation: the architecture is now implemented. See [harness/RECOVERY.md](../../harness/RECOVERY.md) for the concrete API, migration notes, and implementation refinements. The sketches below preserve the design discussion.
Status: implemented, with concrete refinements linked above. This refines
[STREAMING_DESIGN.md](../../STREAMING_DESIGN.md) and
[ADR-002](ADR-002-recoverable-session-log.md). Go fragments show package-level
shapes; they are not standalone source files. Existing domain requests and
responses remain in their current packages unless explicitly described here.

Final design audit: 2026-09-12, against `c93c514`. The corrections and remaining
implementation proof obligations are recorded at the end of this document.

## Package boundaries

| Package | Responsibility |
| --- | --- |
| `identity` | Shared identifiers without domain or storage dependencies |
| `provider` | Model input, incremental text, and validated final response |
| `agent` | Runtime output lifecycle, history commitment, and typed reporting |
| `conversation`, `work` | Routing and workflow facts; no storage DTOs |
| `eventlog` | Record envelope, store contract, ordered publication, subscriptions |
| `harness/record` | Versioned payload DTOs, including content references |
| `harness/eventcodec` | Runtime facts to/from record DTOs; content framing |
| `harness/projection` | Deterministic view reduction and read models |
| `harness` | Ownership, public commands, subscriptions, and authorized content reads |

`harness/record` and `harness/projection` do not import the parent `harness`
package. Domain code does not import either of them or `eventlog`. A session binds
runtime reporters through adapters. The same Go reducer serves the TUI and server
inspection; HTTP clients can implement the documented record semantics themselves.

Use interfaces at replaceable collaborator boundaries. Session, publication Log,
Subscription, and the default Projector are concrete types. There is no universal
Session interface, Snapshot interface, or second subscription abstraction.

## Identity and envelope

```go
// package identity
type SessionID string
type ActorID string // Existing type.
type MessageID string
type ToolInvocationID string
type ContentID string

type OutputID struct {
    Agent ActorID
    Call  uint64
}
```

Move the declaration of MessageID down to `identity`, preserving a `message.MessageID`
alias. Work/plan/submission IDs stay in `work`. Output IDs are unique within a
session; cursors carry the session identity. IDs are assigned by the runtime,
never inferred from provider tool-call names or client-supplied actor fields.

```go
// package eventlog
type Cursor struct {
    Session  identity.SessionID
    Sequence uint64
}

type Correlation struct {
    Agent      identity.ActorID
    Message    identity.MessageID
    Output     *identity.OutputID
    Invocation identity.ToolInvocationID
}

type Data struct {
    Kind        string
    Time        time.Time
    Correlation Correlation
    Payload     json.RawMessage
}

type Record struct {
    Version uint32
    Cursor  Cursor
    Data
}
```

The public JSON uses explicit snake_case fields and tagged payload DTOs. The wire
version covers the envelope and its required payload schemas. Backend code treats
payloads as opaque JSON; the codec and reducer validate their domain meaning.
Cursor sequence zero is the empty prefix, not a stored record. Only the all-zero
cursor is shorthand for this session's beginning; other cursors must name it.
The log supplies sequence/version, validates timestamp/identity, and freezes data.
Time records when the fact occurred, not when a reconnecting view reads it.

## Recoverable store

```go
// package eventlog
type StoreState string

const (
    Writable StoreState = "writable"
    Sealed   StoreState = "sealed"
    Failed   StoreState = "failed"
)

type Problem struct {
    Code    string
    Message string
}

type Head struct {
    Cursor  Cursor     // Last completely readable record.
    State   StoreState
    Failure *Problem   // Present when failed; may be outside the record stream.
}

type ReadQuery struct {
    After      Cursor
    MaxRecords int
    MaxBytes   int
}

type Page struct {
    Records []Record
    Next    Cursor     // Last record returned, or normalized After.
    Head    Head       // Head observed for this read.
}

type Store interface {
    Append(context.Context, Data) (Record, error)
    Read(context.Context, ReadQuery) (Page, error)
    Head(context.Context) (Head, error)
    Wait(context.Context, Cursor) (Head, error)
    Fail(error)
    Seal(context.Context, Outcome) (Record, error)
    Close(context.Context) error
}
```

Each store is created for exactly one session. A single session-owned publication
worker calls Append/Seal. Reads, waiting, and Fail may be concurrent. Close satisfies
the existing Resource contract. Exposing a Store factory does not expose an owned
store's mutation methods to SDK clients.
Session construction validates that its store names the expected session, is
Writable, and has an empty prefix before accepting startup records. A read-only
interrupted archive is finite incomplete input, not a Writable store that waits
for a nonexistent producer. Archive inspection cannot implicitly resume execution.

- Append accepts one complete record and returns its readable cursor. It never
  evicts accepted records or substitutes omissions. Large content is framed before
  Append. Invalid input rejected before write has no effect; ambiguous backend
  write failure latches Failed and is not retried.
- Read returns whole records within both budgets. If the next record cannot fit,
  return a specific page-budget error with its required size rather than an empty
  page that looks caught up. Subscription budgets support the maximum record size.
- Wait returns immediately if head is beyond its argument or the store is sealed
  or failed. Otherwise it atomically checks/registers for change. A future or
  wrong-session cursor is an error. Wait does not consume records or register a
  second long-lived subscriber.
- Fail latches publication failures that originate above the backend, such as an
  encoding failure, and wakes readers. It preserves the accepted readable prefix,
  forbids further writes, and does not fabricate a terminal record. Backend write
  failures perform the same transition. Subsequent failures preserve the first
  cause; Fail does not change an already sealed or closed store. An exporter
  failure after a successful seal cannot rewrite the session outcome.
  Fail updates/wakes the store's status without waiting on backend I/O. If it wins
  before an in-flight append/seal commit, that write must not advance the readable
  head when it eventually returns. If commitment won first, its accepted record
  remains in the prefix. Physical I/O completion is joined separately.
- Seal appends exactly one terminal session record using the next sequence and
  freezes the log. Repeated sealing with the same outcome returns the same record;
  a conflicting outcome errors. A failed store cannot be successfully sealed.
  Expose the terminal record, new head, and Sealed state at one commit boundary,
  after the backend's required seal/sync work succeeds. A seal failure leaves
  Failed plus the previously accepted prefix; bytes from an ambiguous attempted
  terminal write are not exposed as an accepted successful terminal record.
- A failed store permits reads of its known readable prefix where storage remains
  accessible. Subscriptions drain that prefix and then report failure. Corruption
  or inability to read the prefix fails immediately. Neither condition is EOF.
- Close follows session disposal: stop read admission, cancel/join readers, then
  close storage. It does not initiate agent shutdown or silently remove archive files.

Outcome evolves the existing `eventlog.Outcome`: initiating reason, execution and
cleanup errors, cleanup attempts, and recovery failure. An omission count cannot
stand in for required data. A backend's append/sync durability is configuration,
not something inferred from readable Head. Initial disk support can retain the
current sync-on-seal policy without promising process-crash recovery.
Problem.Message and terminal control fields are bounded summaries. Full captured
execution-error text can use a correlated diagnostic record and content reference;
it must not make the terminal record arbitrarily large. A failed store may be
unable to retain new diagnostic details, which must remain explicitly unavailable.

## Publication versus observation

```go
// package eventlog: concrete session-owned coordinator
func (l *Log) Publish(ctx context.Context, data Data) (Cursor, error)
func (l *Log) Finish(ctx context.Context, outcome Outcome) (Cursor, error)
func (l *Log) Fail(err error)
```

Publish waits for accepted storage, not merely queue admission. Log owns bounded
ingress, the independent writer, failure propagation, and drain lifetime. Its
writer does not call agents, workflow dispatch, or external observers. Backend
failures initiate session cancellation through a nonblocking failure notification.
The publication budget includes admitted data, the in-flight write, and owned
copies/encoding buffers. Acquire bounded capacity before making queue-owned copies;
limit concurrent pending producers so blocked callers cannot form an unbounded
queue outside the queue. This bounds publication machinery, not all agent history
or caller-owned inputs. Operation/HTTP admission has its own limits.

Caller cancellation before admission prevents publication. After admission,
cancellation can stop that caller's wait without withdrawing the queued record;
it is not evidence of rollback. Required reporting after a domain transition uses
the session's publication/drain lifetime. Stable entity identities permit inspection
after an ambiguous host wait; neither Log nor an HTTP adapter retries it blindly.

```go
// package agent: replaces required fire-and-forget execution callbacks
type Event interface { isAgentEvent() }

type Reporter interface {
    Publish(context.Context, Event) error
}
```

Agent events cover lifecycle, history append, output, tool activity, usage, and
consumption facts. Their payloads are runtime/domain values, not record JSON or
content references. A controller-bound adapter supplies/validates agent identity,
then forwards into the shared codec/publication path. Conversation and workflow
have corresponding typed adapters over their existing event families; no generic
`any` callback or storage dependency is pushed into domain packages.
Harness-owned agents always receive a required Reporter. Low-level agent hosts
that deliberately supply a no-op implementation do not obtain session recovery.

This is an intentional change to callback semantics. For an entity transition,
serialize emission order, mutate/copy the fact under its short-lived state lock,
release that state lock, and then publish. Do not merely replace existing
OnLifecycle/OnTool function bodies with blocking writes. The emission sequencer
must not be needed by the writer, a read model, or the failure/cancellation signal.
The workflow event consumer cannot be the worker acknowledging its own writes.

Stop and failure signal execution cancellation without waiting for storage or the
emission sequencer. State mutation/cancellation acceptance and response commitment
use a short local control boundary; no I/O or publication wait holds it. Ordered
stop/lifecycle reporting can finish later using the drain lifetime. A successful
control acknowledgment still waits for its required record. Backend failure must
wake blocked publication callers independently of the writer: Log.Fail latches
Store.Fail, cancels active writes, and fails queued completions without waiting
for its own publication queue. A late backend result cannot overwrite failure.
A backend ignoring its deadline is an outstanding resource, not a confirmed
completed close.

An error after domain mutation is a failure to make the transition recoverable;
it does not undo that mutation or any external effect. Stop subsequent execution
and expose the failure. Successful command receipts wait for their required facts;
dependent activity cannot publish consumption before message acceptance.

## One public subscription

```go
// package harness
type SubscribeOptions struct { After eventlog.Cursor }

func (s *Session) Subscribe(context.Context, SubscribeOptions) (*eventlog.Subscription, error)

// package eventlog: concrete shared implementation over Store
func (s *Subscription) Next(context.Context) (Record, error)
func (s *Subscription) Close()
```

Next reads a bounded page after its cursor, serves its buffered records, and calls
Wait only when no record is available. It checks terminal/failure status after
draining the readable prefix. Append between Read and Wait is covered by Wait's
predicate, including Seal with no further producer activity. No lease is needed
because the log retains history for the session lifetime.

The attachment context and Close own only the subscription. A Next-call context
owns one wait. Successful Next advances the subscription's delivery cursor, while
the consumer separately tracks the last successfully applied cursor for reconnect.
An application crash between delivery and application therefore does not skip data.

HTTP maps these same records and exclusive cursors to NDJSON. Use named session
authorization for replay and content access. Initial replay needs no mutable
configuration fetch to render earlier records correctly. Historical schema and
configuration records determine their interpretation.
Subscription Close is idempotent and cancels its pending Next/backend read;
disposal cancels and joins all admitted reads before closing storage. Register
the store as one owned resource, not independently under both Log and Session.

## Streaming types

```go
// package provider
type Delta struct { Text string }

type Observer interface {
    OnDelta(Delta) error
}

type Provider interface {
    Submit(context.Context, Request, Observer) (Response, error)
}
```

Request and Response retain their existing content/tools/usage fields. The agent
provides an Observer bound to one output. Serial callbacks contain an append-only
UTF-8 prefix; callback errors stop reading and fail the submission. The callback
checks execution cancellation before accepting new text into the coalescer.
Accepted text, including a callback racing stop, is published using the drain
lifetime and awaited before calculating terminal Bytes. Do not use an ambiguous
canceled Publish wait to guess whether the last delta should be sent again.
Nil observation does not change execution policy. Non-streaming providers can emit
no callbacks; the agent publishes the final validated text in bounded chunks.
An asynchronous coalescer failure is latched by the call owner and cancels active
provider I/O even if no further callback arrives. A successful Submit return cannot
override that failure: join/check the coalescer before history commitment. Empty
protocol deltas are filtered by the adapter; reasoning-only fields are not silently
mixed into assistant text. Deadline expiry is a failed generation with a timeout
code; intentional stop/owner cancellation is canceled. A storage/publication error
remains a recovery failure even if cancellation subsequently ends the provider call.

```go
// package agent: representative members of Event
type OutputStatus string

const (
    OutputActive    OutputStatus = "active" // Projection state after start.
    OutputComplete  OutputStatus = "completed"
    OutputFailed    OutputStatus = "failed"
    OutputCanceled  OutputStatus = "canceled"
)

type OutputStarted struct {
    Output          identity.OutputID
    ContextRevision uint64
    StartedAt       time.Time
}

type OutputDelta struct {
    Output identity.OutputID
    Offset uint64 // UTF-8 bytes before Text.
    Text   string
}

type OutputFinished struct {
    Output          identity.OutputID
    Status          OutputStatus // Terminal values only.
    Bytes           uint64
    HistoryPosition *uint64      // Present only on completed output.
    Err             error       // Runtime error, encoded as code/message.
    FinishedAt      time.Time
}

type HistoryAppended struct {
    Position uint64
    Message  provider.Message
    Output   *identity.OutputID // Nil for non-output entries.
}
```

Start is accepted before Submit begins. Each delta's offset equals the accepted
length so far; offsets are bytes rather than token counts. The agent fills any
remaining final-response suffix and rejects a conflicting response. Flush/join
coalescing before finish. Terminal Bytes equals the complete published prefix,
including on failure. Finish need not duplicate the entire generated string.

Successful finish references an accepted HistoryAppended position. Failure and
cancellation retain output text without committing it as an assistant message.
Completion is ordered against cancellation at the agent's local commit boundary;
later cancellation affects subsequent work, not the committed output status.
An output with tools is complete before tool execution begins. Usage remains a
separate event correlated to the same call; replay does not increment it twice.
The reducer accepts only started -> delta* -> one terminal finish, matching byte
counts and valid history references. No delta follows finish. Completed output has
a history position and no output error; failed/canceled output has no history
position and a classified reason. A log can end after history commitment but
before output_finished if storage/process failure intervenes. Preserve that
accepted history entry and expose incomplete observation; do not synthesize either
a successful finish or a canceled, uncommitted result from that prefix.

## Record DTOs and large content

`harness/record` owns wire-safe equivalents of the runtime facts. It has explicit
JSON tags, validated discriminators, and code/message errors instead of Go errors.
Reuse domain IDs and enums where their dependencies permit. Avoid a second set
of execution rules in the codec or the record package.

```go
// package eventlog: a reference into the same session log
type ContentRef struct {
    ID     identity.ContentID
    First  Cursor
    Last   Cursor
    Bytes  uint64
    SHA256 string
}

// package harness/record
type ContentChunk struct {
    ID     identity.ContentID
    Offset uint64
    Data   []byte // Base64 in JSON; record budget includes encoding overhead.
}

type Text struct {
    Inline  *string
    Content *eventlog.ContentRef
}

type OutputFinished struct {
    Output          identity.OutputID
    Status          agent.OutputStatus
    Bytes           uint64
    HistoryPosition *uint64
    Error           *eventlog.Problem
    FinishedAt      time.Time
}
```

Text has exactly one representation, including an explicitly empty inline string.
Large tool arguments, errors, messages, and history content use references rather
than unbounded record bodies. Images retain MIME type plus a binary ContentRef.
Malformed tool arguments remain text; they must not be inserted into a JSON
payload as invalid RawMessage. Structured wide values use versioned DTO encoding
and bounded framing rather than dropping fields to fit.
Record schemas keep the bounded control fields needed by the reducer inline;
large display content stays referenced. A schema must not require an unbounded
blob download inside Apply merely to discover the transition's identity or status.

Content chunks may interleave with other records. First/Last bound their record
range; matching ID and contiguous byte offsets identify the chunks. Verify byte
length/hash when committing or reconstructing the full content. A partial page
does not prove a whole-content hash. ContentRef cursors name the same session and
precede the referring record. Chunk publication alone does not create a timeline
entry; an interrupted unreferenced object remains inert.

All content writes are codec/publication implementation details. Consumers can
resolve an immutable ContentRef using bounded reads through Session; they cannot
use it as a filesystem path or modify a content object. Backends may maintain
indexes, but those indexes are rebuildable from the one log.

Routing records carry OutputID in their host metadata. Do not add it to the JSON
envelope sent to a model. The same identity links the final output, commentary,
history entry, and delivered reply without duplicating the visible message.

## Projections and finite inspection

```go
// package harness/projection: concrete reducer, no execution dependencies
func New(session identity.SessionID) *Projector
func (p *Projector) Apply(eventlog.Record) error
func (p *Projector) Cursor() eventlog.Cursor
func (p *Projector) Output(identity.OutputID) (OutputView, error)

type OutputView struct {
    ID              identity.OutputID
    Status          agent.OutputStatus
    ContextRevision uint64
    HistoryPosition *uint64
    TextBytes       uint64
    Through         eventlog.Cursor
    Error           *eventlog.Problem
}
```

Apply validates and updates one record atomically with its cursor. A wrong session,
gap, unknown required kind, invalid payload, or inconsistent output offset leaves
both state and cursor unchanged. Records at/before the applied cursor are ignored
for reconnect, relying on the store's immutability guarantee. Apply never calls a
provider, tool, router, or mutation API. One owner serializes mutation; reads return
independent values under internal synchronization. Views format those values.

Large text is read by output identity and a fixed prefix instead of copying a
growing string into each OutputView. Output deltas are themselves retained text;
their stable IDs/offsets form a rebuildable text index. Completed history can
reference stored content without requiring the TUI to materialize every image.

```go
// package harness
type OutputTextQuery struct {
    Output   identity.OutputID
    Through  eventlog.Cursor // Prefix returned by output inspection.
    Offset   uint64
    MaxBytes int
}

type TextPage struct {
    Through eventlog.Cursor
    Offset  uint64
    Text    string
    Next    uint64
    End     bool
}

type OutputInspection struct {
    Output projection.OutputView
    Source eventlog.Head
}

func (s *Session) InspectOutput(ctx context.Context, id identity.OutputID) (OutputInspection, error)
func (s *Session) ReadOutputText(ctx context.Context, q OutputTextQuery) (TextPage, error)
```

InspectOutput waits for the projection to apply at least the readable log head
captured when the call began, or returns the caller's context/error. It reports
the actual applied cursor, which may be newer. Source is sampled after reading
the projection and has a cursor at least as new as Output.Through. Its availability
is separate from the deterministic state at that prefix; no runtime reads are
mixed into either value. A failed source can return its last reconstructable
output together with Source.Failure. An active status there means active at the
recorded prefix, not confirmed ongoing generation. Views show recovery failure
instead of an indefinite spinner. A reducer failure is returned as an explicit
projection error and wakes inspection waiters; it must not leave them waiting
forever for an unattainable cursor.
ReadOutputText uses the returned Through boundary even if the output has since
completed. Its pages end on valid UTF-8 boundaries; invalid byte offsets and a
budget too small for the next code point return explicit errors. End means end
of text at Through, not end of generation.

Ordinary content reads use byte offsets and byte pages, since they also serve
binary images:

```go
// package harness
type ContentQuery struct {
    ID       identity.ContentID
    Offset   uint64
    MaxBytes int
}

type ContentPage struct {
    Ref    eventlog.ContentRef
    Offset uint64
    Data   []byte
    Next   uint64
    End    bool
}

func (s *Session) ReadContent(context.Context, ContentQuery) (ContentPage, error)
```

Resolve ID from accepted referencing records or their rebuildable index, not
caller-supplied paths, byte ranges in a file, or an unverified ContentRef. No
committed reference means content is not yet available through this method,
even if raw chunk records exist. A reused ID with conflicting metadata is invalid.
Reads validate session authorization, offset and budget, return independent bytes,
and remain fixed to that immutable object. A budget/offset error is distinct from
not-found, disposed, unavailable storage, and integrity failure. ReadContent waits
only for indexing through the head captured on entry; it does not wait indefinitely
for a future object to be published.

Agent, message, work, and transcript projection queries follow the
same finite-read principle: stable identities/positions, bounded pages, and the
applied cursor. These inspection methods are optional reads, never a bootstrap
requirement for Subscribe. A future checkpoint can accelerate Projector without
changing the client contract or becoming a store-owned domain Snapshot interface.

## Interface audit

| Risk | Contract that addresses it |
| --- | --- |
| Store wakeup race | Predicate-based Wait includes already committed append, seal, and failure |
| Failed store hides its previously accepted prefix | Readable prefix drains before reporting failure; read corruption still fails immediately |
| Fixed page budget loops forever on a larger record | Explicit page-budget error and subscription budget compatible with maximum record size |
| Cursor advances before client application | Subscription delivery cursor is separate from the consumer's last applied cursor |
| Required publisher becomes a blocking observer | Reporter/Log are execution collaborators; external observers only get subscriptions |
| Existing under-lock callbacks deadlock | Emission sequencing outside state locks; independent writer, no execution callbacks |
| Native streaming callback cannot report storage failure | Observer.OnDelta returns error; Submit stops on callback failure |
| Failed partial text disappears | Terminal output retains byte length and indexed deltas independently of model history |
| Giant output inspection copies all text | OutputView metadata plus fixed-prefix, UTF-8-safe text paging |
| New IDs introduce domain/storage cycles | Shared IDs in identity; runtime facts separate from versioned record DTOs |
| Content references imply another source of truth | Chunks and referencing facts belong to the same retained log; indexes are rebuildable |

These contracts intentionally change Provider, required reporting, store retention,
and subscription signatures. Migrate all in-repo adapters/fakes together, document
the public API break, and retain legacy archives only under their declared coverage.
No production interfaces are added by this documentation stage. The acceptance
suite in STREAMING_DESIGN remains the implementation gate.

## Final audit corrections and proof obligations

The core architecture is unchanged. This pass closes failure/race gaps rather
than adding another state authority or bootstrap API.

| Priority | Finding | Correction / required test |
| --- | --- | --- |
| P1 | A seal could expose a terminal record before sync failed | Terminal record/head/Sealed become visible together after successful seal; inject failure between terminal write and sync |
| P1 | Queued failure reporting could wait behind a stuck write | Concurrent non-I/O Fail path; blocked callers wake, late writes cannot advance a failed head, cleanup still joins I/O |
| P1 | Stop could wait on a reporter blocked by storage | Cancellation bypasses emission waits; test stop while publication is saturated and while commitment races cancellation |
| P1 | Accepted history may outlive a missing output_finished | Preserve the accepted prefix and report source failure, never invent an output terminal transition |
| P1 | Coalescer failure could be lost when no further token arrives | Cancel provider on asynchronous failure and check the joined coalescer result before committing history |
| P2 | A bounded queue could still accumulate unbounded waiting copies | Account for encoding/in-flight buffers and bound pending producer admission |
| P2 | Failed observation could look like an output still running | OutputInspection separates prefix state from source health; reducer failure wakes waiters |
| P2 | Large content had no concrete public read method | ReadContent by immutable ID, byte paging, scoped authorization, and reference/integrity validation |

Implementation must prove these contracts with controlled interleavings, not
assume that moving callbacks outside mutexes is sufficient. Validate the package
dependency graph, every required record family, store conformance for memory/disk,
and projection equivalence at a common cursor. Numeric limits, indexing data
structures, and the precise lock/ownership implementation remain implementation
choices; they must meet these fixed acceptance criteria. No runtime correctness
claim follows from this documentation audit alone.
