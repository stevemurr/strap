# Streaming and session recovery

A session owns execution and one retained log. A view subscribes to that log; it
can detach, reconnect, or rebuild from the beginning without affecting agents.
Required facts are acknowledged only after storage can read them. Recording
failure cancels execution and leaves an explicit failed head plus the readable
prefix. It never substitutes an omission or a successful terminal record.

```go
sub, err := session.Subscribe(ctx, harness.SubscribeOptions{
    After: eventlog.Cursor{}, // Beginning of this session.
})
if err != nil { return err }
defer sub.Close()
view := projection.New(identity.SessionID(session.ID()))
for {
    record, err := sub.Next(ctx)
    if errors.Is(err, io.EOF) { break }
    if err != nil { return err }
    if err := view.Apply(record); err != nil { return err }
    applied := view.Cursor() // Save this cursor for reconnect.
    _ = applied
}
```

The attachment context and `Close` detach only the subscription. A `Next` context
limits one wait. Resume after the last successfully **applied** cursor. Delivery
alone does not mean the view applied the record. Wrong-session and future cursors
are rejected. Records already applied are ignored; gaps and invalid required
records leave projection state and its cursor unchanged.

`InspectOutput(ctx, id)` returns metadata at `Output.Through`, alongside source
health sampled afterwards. `ReadOutputText` pages that fixed prefix by UTF-8 byte
offset, even if generation has advanced. A page ending at that prefix does not
mean generation ended. An active output with a failed source describes the last
recoverable state, not a confirmed running generation. Failed/canceled text stays
in the log; it is not appended as successful assistant history. If recording fails
between history commitment and output finish, the history remains committed and
no terminal output state is invented.

Large payloads use `content_chunk` records followed by a referring fact containing
`content_ref` and bounded `control` metadata. The reference includes session
cursors, byte count and SHA-256. Chunks alone create no visible domain entity.
`ReadContent(ctx, ContentQuery)` accepts a committed content ID, validates integrity,
and returns bounded byte pages. `ResolveRecord` explicitly materializes a full
payload for a view; `eventcodec.DecodeEvent` rejects unresolved framed records.
The reducer uses control metadata and never fetches content during `Apply`.

Agent inspection/transcripts and work reads derive from accepted records. Work
inspection currently replays the finite work-fact index into a passive read model;
this favors correctness over repeated-query performance. It shares the ledger's
existing actor visibility rules and cannot dispatch anything. No inspection call
resumes execution. The TUI uses the shared reducer, appends streaming text to one
output row, and reconciles replies/commentary and optimistic input by stable IDs.
Reasoning renders separately and expands initially, then collapses on first answer
content unless the user has made an explicit F3 choice. That preference applies
across output rows. The transcript browser's `t` mode reads reasoning from the log
at fixed cursors, including calls with no committed assistant-history entry.
Reads run asynchronously; snapshots refresh with `r` and page through older calls.
Clearing main display rows does not clear the shared projector or retained log.

## Reasoning and answer channels

`provider.Delta.Channel` distinguishes `content` from `reasoning`. Existing Go
providers may omit the channel to mean content; the harness always records the
normalized explicit value. Unknown channels fail. The adapter recognizes
`reasoning` and `reasoning_content`: null/missing fields are absent, identical
values in both fields are emitted once, and conflicting values fail decoding.

Both channels share an OutputID and lifecycle with independent UTF-8 offsets,
prefix validation and terminal byte totals. `Bytes` / `TextBytes` remain answer
content counts; `ReasoningBytes` counts recorded reasoning. `ReadOutputText`
accepts `Channel` (default content) and returns a page identifying that channel at
the fixed `Through` cursor. Neither a page end nor arrival of content establishes
that reasoning has finished. Channels may interleave until output termination.

One bounded coalescing buffer flushes on channel changes, size or the timer. The
log preserves accepted observation order, not an inferred generation order.
Complete JSON responses and unobserved final suffixes use reasoning-then-content
order; both final prefixes validate before either suffix is published. The
adapter still assembles complete response strings, including reasoning, so its
peak response memory is not bounded by the coalescing buffer.

Reasoning is required retained observation data, including accepted partial text
on failure/cancellation. It is never copied to `provider.Message`, routed answer
or commentary content, or subsequent model requests, and it does not advance
history revisions. Provider-reported generation usage remains authoritative.
A reasoning-only response cannot commit successful assistant history. Recording
failure uses the same failed-log and execution-cancellation contract as content.

## Storage and lifecycle

Default memory storage retains the entire session. `Events.Retention`, retained
as a configuration name for compatibility, now sets an optional **hard quota**;
zero means unlimited. Reaching a quota fails recording instead of evicting history.
`Events.JSONLPath` selects an exclusively created disk archive with a rebuildable
temporary disk index. It bounds backend working buffers, not agent history or the
complete response returned by a provider. There is no SQLite implementation yet.

The default publication queue is 1,024 records / 8 MiB, including in-flight work.
Each backend write has a 15-second deadline. Encoding admits at most 64 producers,
uses bounded string/binary buffers, and frames bodies into chunks of at most
64 KiB (smaller for smaller configured queues). Commands and storage reads admit
at most 256 concurrent operations; HTTP defaults to 256 requests, including open
streams, and permits a host-configured limit. Excess admission returns an explicit
busy error. Agent text shares a 16 KiB coalescing buffer across channels, flushing on
channel changes or every 40 ms. Failure of a timer-driven flush cancels provider I/O even without another
callback; flush/join completes before history commitment.

`Close` stops execution, joins producers and telemetry, closes execution resources,
then seals the log. `Dispose` additionally cancels/joins storage readers and closes
the store. A writer ignoring cancellation remains an owned resource; a timed-out
close does not pretend that I/O finished. `Store.Fail` returns its atomically
observed head so a late failure cannot rewrite a seal that already committed.
JSONL sync-on-seal is not process-crash execution recovery. Reopened unsealed
archives are finite failed input; they cannot silently start running again.

## API migration

This implementation writes schema **3**. Readers also accept schema 2, normalizing
its output deltas to content-only without rewriting stored records or hashes.
Schema 2 does not establish whether the provider generated reasoning; it did not
retain that channel. Schema-3 deltas require an explicit valid channel. Older
readers reject schema 3. Schema-1 archives have narrower coverage
and are rejected by the new archive reader/reducer rather than being presented as
fully recoverable sessions. Provider implementations now implement
`Submit(context.Context, provider.Request, provider.Observer)`. Nil observation
uses the same execution policy. Both HTTP model adapters request SSE, assemble
fragmented tool calls, and validate completion before returning executable calls;
compatible servers returning complete JSON are also accepted.

The proposal's interface sketches were refined as follows:

- The stored envelope retains `schema`, `session`, `sequence` and exposes `Cursor()`;
  `Record` aliases `Event`. Output and message correlations are explicit fields.
- Finite `Store.Read` keeps `Query{After, Limit, MaxBytes}` within its bound session;
  `Head`, `Wait`, and public subscriptions use complete session-scoped cursors.
- `Log.PublishContext` returns the readable cursor; `Publish(Data)` is its synchronous
  shorthand. `Seal`/`Finish` return errors; `Head` exposes their committed cursor.
- Wide DTOs are framed as complete JSON bodies with bounded transition metadata,
  including binary image data. This avoids truncating nested work/tool evidence.
- Inspection catches the shared projector up to a finite captured head on demand;
  it does not require a second background observer queue or a bootstrap snapshot.

See [HTTP routes and errors](httpapi/README.md),
[the design](../STREAMING_DESIGN.md), and the executable acceptance tests in
[recovery_test.go](recovery_test.go),
[httpapi/recovery_test.go](httpapi/recovery_test.go),
[agent/output_test.go](../agent/output_test.go), and
[eventlog/recovery_test.go](../eventlog/recovery_test.go).
