# Tool execution recording

The current proposed improvements are in
[Session trace improvements: types and contracts](docs/architecture/TRACE_CONTRACTS.md).
That plan extends the implemented session log with structured outcomes, provider
evidence, recording placement, and execution provenance.

Historical design sketch. Session-owned recording is now implemented in
`eventlog`, `harness/eventcodec`, and `harness`; this document retains the original proposal. Snippets show the intended boundaries, with supporting implementation
explicitly described below. They are not a complete patch.

The [public harness design](HARNESS_DESIGN.md#session-diagnostics) now supersedes
this document's CLI-owned recorder and workflow-observer wiring. Its diagnostic
capture examples remain design input; implementation should use the session's
canonical publication/storage path rather than add a parallel event stream.

Record tool execution to one JSONL file per session so an edit failure can be
examined alongside its exact arguments and the file text that was searched.
The terminal and model continue to receive the existing short error message.

## Ownership and scope

```text
cmd/strap: opens recorder, wires observer, owns cleanup
  conversation.Controller: emits existing execution events
    workflow.Session: sole controller event reader
      recorder.Observe(event): bounded in-memory admission
        internal/recording: one writer -> session.jsonl
      existing workflow processing and TUI relay
```

The controller does not acquire a logger, file handle, or recorder dependency.
The workflow accepts an optional callback, not a dependency on the recording
package. The callback runs in its existing controller reader, before forwarding.
The recorder filters the event types it supports. This first version records tool
starts and completions, plus session start/end records. Synthesized workflow
events are outside this version; a future expansion must include their publishing
path explicitly.

Record successful calls as well as failures: a successful read followed by
another agent's successful edit can explain a later failed replacement. Record
ordered text results, and image MIME type and byte count; do not inline image
bytes in this first version. This is a tool execution record, not an exact model
request archive. Prompt diagnosis eventually needs separate request snapshots
with model configuration, tool definitions, and history revision attribution.

## 1. Capture the text at the point of failure

Add `tool/edit_error.go`. Keep the error immutable, with a value-returning
accessor. Do not add debug information to `tool.Result.Content`: that content
enters the model's history.

```go
// In package tool.
type EditFailure struct {
    Code         string `json:"code"`
    ResolvedPath string `json:"resolved_path"`
    ObservedText string `json:"observed_text"`
}

type EditMatchError struct {
    message string
    detail  EditFailure
}

func (e *EditMatchError) Error() string { return e.message }
func (e *EditMatchError) DebugSnapshot() EditFailure { return e.detail }

func editMatchError(code, message, path, text string) error {
    return &EditMatchError{
        message: message,
        detail: EditFailure{
            Code: code, ResolvedPath: path, ObservedText: text,
        },
    }
}
```

Inside `Files.edit`, replace the two matching errors using the already-read
`text` and already-resolved `path`:

```go
// Replacement fragment inside Files.edit, after readText succeeds.
func exampleMatchingCheck(text, old, path string) error {
    first := strings.Index(text, old)
    if first < 0 {
        return editMatchError("text_not_found",
            "old text was not found", path, text)
    }
    if first != strings.LastIndex(text, old) {
        return editMatchError("text_ambiguous",
            "old text matches more than once; include more surrounding text",
            path, text)
    }
    return nil
}
```

The actual implementation keeps the existing matching code in `Files.edit`;
the function above just makes the replacement fragment readable. No new read or
matching policy is introduced. The snapshot is the string actually searched,
including whitespace and line endings. Files already limits this read to its
configured maximum (1 MiB by default). Its lock serializes operations using this
Files instance, but not external editors or shell commands; the snapshot proves
what this operation searched, not global filesystem ordering.

Use `errors.As` when projecting the error to recover this payload through wrapped
errors. Ordinary errors are recorded as their `Error()` string. The existing
agent error-to-tool-result conversion and TUI error renderer remain unchanged.

## 2. Identify each invocation independently of provider IDs

Extend `agent.ToolActivity` with `Invocation uint64` and `ContextRevision uint64`.
Add an agent-local `nextToolInvocation uint64`, incremented only by the sequential
agent loop. At the start of `Agent.call`:

```go
// Illustrative helper used by Agent.call before reporting the start event.
func (a *Agent) beginTool(call provider.ToolCall) ToolActivity {
    a.nextToolInvocation++
    return ToolActivity{
        Invocation: a.nextToolInvocation,
        ContextRevision: a.ContextRevision(),
        Call: call,
        StartedAt: time.Now(),
    }
}
```

The deferred completion copies that activity and adds `FinishedAt`, `Result`,
and `Err`. Both events go through the existing `reportTool` ownership copies.
The new revision identifies history immediately before this tool dispatch,
including the assistant's tool request and any preceding results in the batch.
It is not necessarily the input revision of the generating model call.

Use `(session_id, agent_id, invocation)` as the pairing key. Preserve the
provider's `call_id` too, to locate the corresponding transcript entry. Provider
IDs alone are insufficient because an ID may recur in a later model response.

## 3. Project events to an explicit record schema

Add `internal/recording/record.go`. Avoid marshaling `conversation.Event` or
`error` interfaces directly; use known wire types.

```go
type RawArguments struct {
    // Valid UTF-8 JSON is retained as text, preserving its original formatting
    // and duplicate keys. This field must be parsed for object-level queries.
    JSONText *string `json:"json_text,omitempty"`
    // Used if bytes are not UTF-8. encoding/json encodes []byte as base64.
    Bytes []byte `json:"bytes_base64,omitempty"`
    ValidJSON bool `json:"valid_json"`
}

type ResultPart struct {
    Text       *string `json:"text,omitempty"`
    ImageMIME  string  `json:"image_mime,omitempty"`
    ImageBytes int     `json:"image_bytes,omitempty"`
}

type ToolRecord struct {
    Version         int          `json:"schema_version"`
    SessionID       string       `json:"session_id"`
    Sequence        uint64       `json:"sequence"`
    Event           string       `json:"event"`
    At              time.Time    `json:"at"`
    AgentID         string       `json:"agent_id"`
    Invocation      uint64       `json:"invocation"`
    ContextRevision uint64       `json:"context_revision"`
    CallID          string       `json:"call_id"`
    Tool            string       `json:"tool"`
    Arguments       RawArguments `json:"arguments"`
    DurationNS      int64        `json:"duration_ns,omitempty"`
    Result          []ResultPart `json:"result,omitempty"`
    Error           string       `json:"error,omitempty"`
    EditFailure     *tool.EditFailure `json:"edit_failure,omitempty"`
}
```

Use `tool.started` / `tool.finished` as event names and the activity timestamps,
not the disk-write time. A sequence is assigned to every eligible tool event
before admission. Gaps plus a final dropped-record count disclose omissions;
sequence describes observer order, not a causal ordering across agents.

The projector preserves invalid argument text too: valid UTF-8 goes in JSONText
even when `ValidJSON` is false. Non-UTF-8 bytes use the base64 branch. Arguments
must not be embedded as unchecked `json.RawMessage`, since malformed arguments
would then make the entire record impossible to encode. Never normalize text.

`session.started` records version, session ID, start time, working directory,
model/backend identifiers, and capture limits. `session.finished` reports end
time, accepted/written/dropped counts, drain status, and any recording failure
when the file is still writable. Do not store endpoint credentials or HTTP
headers. The completion record's arguments deliberately repeat the start's so
a failure is useful on its own.

## 4. Recorder API and writer ownership

Add `internal/recording/recorder.go`:

```go
type Config struct {
    Path            string
    SessionID       string
    MaxRecords      int
    MaxPayloadBytes int64
    MaxPendingBytes int64
}

type Status struct {
    Accepted uint64
    Written  uint64
    Dropped  uint64
    Err      error
}

// Proposed API; worker/queue implementation is not supplied in this sketch.
func Open(config Config) (*Recorder, error)
func (r *Recorder) Observe(event conversation.Event)
func (r *Recorder) Status() Status
func (r *Recorder) Changed() <-chan struct{}
func (r *Recorder) MarkIncomplete(err error)
func (r *Recorder) Close(ctx context.Context) error
```

`Open` validates limits and opens a new file with `O_CREATE|O_EXCL|O_WRONLY` and
mode `0600`. Explicit `-record path` enables capture; existing paths fail rather
than being truncated. Session IDs are generated by the application. The initial
session record is written before agent creation so startup write failures are
reported immediately.

`Observe` filters unsupported events and creates an owned projection. It does
no file I/O, waits on neither the writer nor queue capacity, and invokes no
user-defined JSON marshal hooks. The supported usage is one observer producer
with `Status` and `Close` safe concurrently. Projection costs CPU proportional
to the captured payload; “nonblocking” here means no I/O/capacity wait, not zero
latency. Size-check before copying oversized payloads.

Suggested initial limits: 256 records, 4 MiB per projected payload, and 32 MiB
of pending payload. The byte accounting includes all variable strings, byte
slices, and part collections. These bound retained payload, not exact Go heap
usage; encoding can expand escaped text and temporarily retain another record.
Keep the active write charged until finished. On overflow or an oversized
record, drop that record, increment counters, and signal `Changed` once on the
transition into degraded recording. Do not silently truncate evidence.

The writer goroutine alone owns the file and encoder. Each accepted record is
encoded on one physical line. Use an unbuffered file writer initially to avoid
another flush timer; the application queue already decouples execution from
disk. On the first write failure, preserve the error, stop recording further
data, release queued payloads, and signal status. Do not retry appending after
a partial JSON write. The final line may be incomplete in that case.

Admission and Close share a short mutex: Close disables admission once and wakes
the worker to drain. No producer closes a channel that another producer may
send on. Close is idempotent, uses the supplied context only to bound waiting,
and returns write/close failures and a nonzero dropped-count error. After a
timeout the worker still owns cleanup; Close does not concurrently close the
worker's file. A later Close can wait again. A blocked filesystem operation
cannot be assumed cancelable by context. A successful close is not an fsync
durability guarantee, and no drain is guaranteed after abrupt process death.

`MarkIncomplete` ignores nil and retains a non-nil upstream drain error for the
session footer and Close result. Call it after session shutdown and before
closing the recorder. It is safe against late Observe calls after a timeout.

## 5. Observe before forwarding, and drain on shutdown

Add an optional constructor option, preserving existing callers:

```go
// In internal/workflow.
type Option func(*Session)

func WithObserver(observe func(conversation.Event)) Option {
    return func(s *Session) { s.observe = observe }
}

// New gains a trailing opts ...Option, applied before starting s.run.
// The observer contract forbids I/O, blocking on capacity, or session reentry.
```

The present controller-reader goroutine uses `s.ctx`, so canceling a session can
discard queued completion events. Introduce a separately cancelable reader
context derived from `context.WithoutCancel(ctx)`, kept in `s.readCtx`, with
`s.stopReads` as its cancel function. The existing sole reader becomes:

```go
func (s *Session) readController(incoming chan<- conversation.Event) {
    defer close(incoming)
    defer close(s.readerDone)
    for {
        e, err := s.Controller.NextEvent(s.readCtx)
        if err != nil {
            // inbox.ErrClosed is clean EOF. Other errors must be retained
            // as a read/drain failure for Session.Close, not treated as EOF.
            s.setReadResult(err)
            return
        }
        if s.observe != nil {
            s.observe(e)
        }
        select {
        case incoming <- e:
        case <-s.ctx.Done():
            // Stop forwarding/dispatching, but continue recording to EOF.
        case <-s.readCtx.Done():
            s.setReadResult(s.readCtx.Err())
            return
        }
    }
}
```

`readerDone` and the synchronized read-result state are initialized before
starting goroutines. `s.run` retains responsibility for processing forwarded
events and closing its UI relay; it cancels normal processing and joins the
reader on exit. It must not cancel the reader before EOF on the graceful path.

The required Close sequence is explicit: cancel workflow processing; request
controller shutdown; wait for the controller to close its event inbox after all
agents finish; let the reader drain to EOF; join the session. On the caller's
cleanup deadline, stop the reader and report incomplete drain. Retain the
controller's current behavior that a timed-out Close can be retried; a forced
reader stop cannot subsequently recover unrecorded events.

This is the only substantial lifecycle change. A recorder-side flush alone is
insufficient: it cannot recover events the relay never observed. A second
controller consumer or a logger attached to TUI.Update would not solve this.

## 6. Application wiring and terminal behavior

In `cmd/strap`, parse `-record`, open the recorder before creating agents, and
pass `workflow.WithObserver(recorder.Observe)` to `workflow.New`. When recording
is disabled, pass no observer. Keep session and recorder cleanup in one deferred
function with separate bounded contexts derived from `context.Background()`:

```go
// Sketch inside cmd/strap.run after both objects have been constructed.
defer func() {
    sessionCtx, stopSession := context.WithTimeout(context.Background(), 5*time.Second)
    sessionErr := session.Close(sessionCtx)
    stopSession()

    // Conservatively mark any unsuccessful session shutdown as incomplete.
    recorder.MarkIncomplete(sessionErr)
    recordCtx, stopRecording := context.WithTimeout(context.Background(), 5*time.Second)
    recordErr := recorder.Close(recordCtx)
    stopRecording()
    err = errors.Join(err, sessionErr, recordErr)
}()
```

The implementation also needs cleanup if construction fails after Open, and a
disabled-recording path without dereferencing a nil recorder. Do not share an
already-expired session cleanup context with recorder.Close. If session closure
times out, late Observe calls must be safely rejected by the recorder's closed
admission gate; report the session as incomplete.

The TUI optionally listens for recorder status changes via a narrow callback or
options field, not a dependency on the recording package. Show the recording
path once and one concise warning if recording degrades. Rich arguments and
diagnostics remain in the file. Add the invocation identifier to the existing
short error metadata to distinguish multiple failures from the same agent in
one minute. Never print background logger output directly to the terminal.

## Audit of the proposed solution

The following issues were found while reviewing the initial event-sink idea.
They are addressed in the sketch's contracts, but require implementation tests
before this can be called production-ready.

| Finding | Severity | Required resolution |
| --- | --- | --- |
| Current relay cancellation can lose final tool completions | Blocker | Separate reader cancellation from execution cancellation; drain after controller shutdown, bounded by cleanup deadline |
| A logger reading NextEvent alongside workflow consumes events instead of copying them | Blocker | Observe inside the existing sole reader |
| Re-reading a file after an error can capture another agent's changes | Blocker | Snapshot the string used by Files.edit at the matching failure |
| Adding payload to tool.Result would feed diagnostics back into the model | Blocker | Immutable diagnostic error; keep Error() concise; recorder explicitly projects extra fields |
| Provider call IDs may recur across responses | Blocker | Agent-local invocation ID on both start and completion records |
| Item-count limits alone allow large file snapshots to exhaust memory | Blocker | Per-record and pending-byte limits; size-check before copying; explicit drops |
| Raw error/argument marshaling can lose messages or fail an entire record | Blocker | Explicit error strings and a byte-preserving arguments envelope |
| Queue closure races and partial disk writes can corrupt output or panic | Blocker | Serialized admission/close and single writer; stop after first write failure |
| Session cleanup can exhaust the deadline before recorder cleanup begins | Blocker | Separate cleanup contexts and accurate incomplete-session metadata |
| Original tool events remain in the UI event queue and may retain error snapshots | Limitation | This recorder bounds its own queue only; existing controller/UI inboxes are unbounded. Avoid claiming a global memory bound |
| JSONL recording may be incomplete under overload, disk failure, or forced exit | Limitation | Sequence gaps, status counters, explicit warnings and Close errors; do not describe this as a lossless audit journal |
| The file does not contain exact submitted prompts/model requests | Scope limit | Add request capture separately if prompt-level diagnosis is needed; do not reconstruct exact inputs from rendered TUI events |
| Per-session files can grow over a long run | Follow-up | Initial explicit opt-in and file-per-session policy; add disk-size/retention controls if needed |

Acceptance tests for the eventual implementation:

1. Failed edit records the exact old/new arguments and observed file text,
   including CRLF, tabs, newlines and Unicode; change the file before writing
   the record and prove the original snapshot is retained.
2. The TUI and next model request still contain only the ordinary tool error.
   Wrapped edit errors retain diagnostics; ordinary errors remain recordable.
3. Successful reads/edits, validation failures, unknown tools, and canceled tools
   generate the appropriate paired records; repeated provider IDs do not collide.
4. Invalid argument JSON and invalid UTF-8 remain inspectable without invalid
   JSONL; image results are metadata only and text result ordering is preserved.
5. Slow/failing writers do not block workflow progress. Oversized events,
   item/byte overflow, and final drops with no subsequent tool event are visible.
6. Race tests exercise Observe versus Close, retrying Close after a timeout, and
   a partial writer failure; no send-on-closed panic or interleaved JSON records.
7. End-to-end cancellation while a tool runs still records its completion and
   agent shutdown before clean EOF. A noncooperating tool causes an explicit
   incomplete drain, not a false successful session footer.
8. Running without recording preserves existing workflow behavior; startup open
   failures fail clearly, and intermediate construction failures release the file.

Audit conclusion: the layering is appropriate, and a logging-framework migration
is unnecessary. Exact failure snapshots and event-drain ownership are the core
changes; JSONL serialization is the small part. This document is a design review,
not evidence that the proposed runtime or its concurrency behavior has been tested.
