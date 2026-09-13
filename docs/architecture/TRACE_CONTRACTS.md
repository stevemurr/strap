# Session trace improvements: types and contracts

Status: evidence extensions proposed. The prerequisite
[inspection package](../../harness/inspection/README.md) now provides independent
archive/live readers, fixed-prefix queries, and HTTP access. The remaining
instrumentation stages below follow this inspection foundation. This sketch incorporates the
successful trace audit and both subsequent contract audits. It extends the
implemented [recoverable session log](../../harness/RECOVERY.md) and
[streaming contracts](STREAMING_CONTRACTS.md).

The objective is to make execution evidence queryable without changing execution
policy. One session log remains authoritative. SDK, HTTP, TUI, and audit tools
consume its records and projections; there is no additional logger, queue, or
subscription contract. Go fragments describe proposed additions, not complete
source files or APIs available today.

## Ownership and data flow

```text
Host application
  chooses working directory, recording policy, and build identity
    -> harness.Session resolves configuration and owns resources
        -> provider adapters report output, usage, and completion metadata
        -> tools report results, execution facts, and errors
        -> agent owns invocation and output lifecycles
            -> existing codec and session log
                -> shared projections and inspections
                    -> SDK / HTTP / TUI / audits
```

| Layer | Responsibility |
| --- | --- |
| `tool`, `provider` | Typed evidence without storage or UI dependencies |
| `agent` | Authoritative invocation and output lifecycle decisions |
| `harness/eventcodec`, `harness/record` | Explicit wire payloads and frozen publication data |
| `harness/projection` | Deterministic reconstruction, including unavailable evidence |
| `harness` | Resource ownership, construction, subscriptions, and inspection |
| Host applications | Recording policy, application directories, and presentation |

## Tool execution facts and final outcomes

Tools return model-visible content, optional execution facts, and an error through
the existing `Call(context.Context, Call) (Result, error)` interface.

```go
// package tool
type Result struct {
    Content content.Content
    Facts   *ExecutionFacts
}

type ExecutionFacts struct {
    Process *ProcessFacts
}

type ProcessFacts struct {
    ExitCode         *int
    Termination      ProcessTermination
    OutputTruncated  bool
    OutputIncomplete bool
}

type ProcessTermination string

const (
    ProcessExited   ProcessTermination = "exited"
    ProcessCanceled ProcessTermination = "canceled"
    ProcessTimedOut ProcessTermination = "timed_out"
)

type FailureClass string

const (
    FailureRejected  FailureClass = "rejected"
    FailureExecution FailureClass = "execution"
    FailureCanceled  FailureClass = "canceled"
    FailureTimedOut  FailureClass = "timed_out"
)

// Optional classification of this invocation, discovered through errors.As.
type ClassifiedError interface {
    error
    FailureClass() FailureClass
    Code() string
}
```

Classification describes the current invocation. Validation/dispatch wrappers
classify rejection; operation boundaries classify execution and interruption.
Codes are stable detail within a category, not an agent-maintained registry for
inferring categories. Unknown codes retain their supplied category. Unclassified
errors remain supported as generic execution failures.

An inner dependency's deadline does not by itself establish that the invocation's
deadline expired. Boundaries must translate nested failures to the appropriate
invocation classification. They must not blindly expose a nested interruption as
their own. Human-readable messages are never parsed for classification.

Facts describe observed execution, not overall task success. An ordinarily exited
process may have any exit code. Nil `ExitCode` means no normal exit code was
available. Missing process facts mean unavailable, not a fabricated zero exit.
Facts and diagnostics are host evidence; they are not automatically appended to
model history. The shell retains its existing model-visible result format.

```go
// package agent
type ToolStatus string

const (
    ToolCompleted ToolStatus = "completed"
    ToolRejected  ToolStatus = "rejected"
    ToolFailed    ToolStatus = "failed"
    ToolCanceled  ToolStatus = "canceled"
    ToolTimedOut  ToolStatus = "timed_out"
)

type ToolOutcome struct {
    Status ToolStatus
    Code   string
}

type ToolActivity struct {
    // Existing identity, call, timestamps, result, error, and diagnostics.
    Outcome *ToolOutcome
}
```

The agent creates exactly one final outcome. New start records omit it; new
completion records include it. There is no competing outcome in `tool.Result`.

| Evidence | Final invocation outcome |
| --- | --- |
| Validation or dispatch rejects an invocation before executing it | Rejected |
| Invocation-scoped classified error | Corresponding classified outcome |
| Captured process termination reports cancellation or timeout | Canceled or timed out |
| Other returned error | Failed |
| Normal return, including an ordinary nonzero process exit | Completed |

The table specifies precedence. Classification is fixed from evidence at the
operation boundary, before completion publication. A later `ctx.Err()` cannot
retroactively change a successful operation. Contradictory facts and errors are
retained and surfaced as a diagnostic contract violation; the outcome follows
the precedence above rather than silently rewriting evidence. Capture/publication
failure remains a session-log failure, not a replacement tool outcome.

For example, `git diff --quiet` exiting 1 is a completed invocation with exit code
1. The observed `git status` exit 128 is likewise queryable as a nonzero exit,
without claiming that all nonzero exits mean execution failure. Invalid
`update_plan` arguments are a rejected invocation; the corrected call completes.

### Preserve evidence before model-visible conversion

`(Result, error)` may contain useful partial results. Built-in tools must retain
available partial output, execution facts, and diagnostics when an operation
fails, times out, or is canceled. This is bounded by existing output limits;
truncation and incomplete output remain explicit.

The agent records this original result and error before the existing model-visible
error conversion. The converted tool history entry may therefore intentionally
differ from the recorded execution result. The outcome must not cause new retries,
agent termination, or changes to continuation/history policy. Completion means the
invocation completed, not that it achieved the user's objective.

## Provider completion evidence and usage

The provider submission/streaming interface is unchanged.

```go
// package provider
type Provider interface {
    Submit(context.Context, Request, Observer) (Response, error)
}

type CompletionMetadata struct {
    RequestID     string
    ResponseID    string
    ReportedModel string
    FinishReason  string // Original provider value.
}

type Response struct {
    Reasoning string
    Content   string
    ToolCalls []ToolCall
    Usage     *Usage
    Metadata  *CompletionMetadata
}

type Usage struct {
    InputTokens       *int64
    OutputTokens      *int64
    ReasoningTokens   *int64
    CachedInputTokens *int64
}

// package agent
type OutputFinished struct {
    // Existing identity, status, byte totals, history position, error, and time.
    Metadata *provider.CompletionMetadata
}
```

Every provider return path preserves valid metadata and usage already observed,
including `length`, malformed frames, transport interruption, and observer
failure. Evidence not received before a failure remains unavailable. Metadata
does not authorize consuming content or executing tool calls returned with an
error. Accepted streaming prefixes retain their existing recovery semantics.

The provider's finish reason and the harness outcome remain distinct: a provider
can report `stop` while the harness rejects an invalid response. IDs are optional
provider identifiers, not replacements for the harness's agent/call identity.
Only explicit supported metadata is recorded; arbitrary HTTP headers and raw
request/response bodies are outside this stage.

The agent attaches completion metadata to `output_finished`. Usage remains on the
existing usage observation, correlated by agent/call and input history revision.
Do not duplicate accounting in both records. Metadata and usage never enter model
history or future provider requests.

Counts are nonnegative; nil means unavailable and zero means reported zero.
Reasoning tokens are a subset of output tokens; cached input tokens are a subset
of input tokens. Populate these normalized fields only when the adapter can
establish those semantics. Do not infer counts from text bytes or add subsets to
totals. Invalid optional measurements become unavailable without invalidating an
otherwise usable response.

For the first implementation, the new breakdowns remain **per-call only**.
Existing aggregate input/output totals and missing-call counts remain unchanged.
Future aggregate breakdowns must include corresponding missing-measurement counts;
they cannot expose partial sums as complete session totals.

Metadata is recorded when `Submit` returns and its completion event is accepted.
Process-crash preservation before that boundary would require an additional
provider observation contract and is deferred. An interrupted archive does not
invent a completion event or metadata that was never recorded.

## Host-controlled recording selection

The host decides whether and where to record. The harness creates and owns the
selected store, preserving its existing seal/disposal contract.

```go
// package harness
type EventConfig struct {
    // Existing retention and publication queue settings.
    JSONLPath      string // Existing explicit destination.
    JSONLDirectory string // New automatic session-ID filename destination.
}
```

| Selection | Storage behavior |
| --- | --- |
| No JSONL selection and no injected store factory | Existing memory store |
| Explicit JSONL path | Exclusively create that file |
| Automatic JSONL directory | Exclusively create `<session-id>.jsonl` there |
| Injected store factory | Existing custom store ownership contract |

These selections are mutually exclusive and conflicts fail before resource
acquisition. Paths are resolved once; existing explicit relative paths retain
their interpretation relative to the host process directory, independently of
the tools' working directory. The resolved file path is visible in session
configuration/inspection and CLI startup information.

Hosts must distinguish option presence from its string value. Selecting automatic
recording and then failing to resolve a nonempty application state directory is
an error, never an implicit fallback to memory. Likewise, explicitly selecting an
empty file path is rejected by the host. Validate that intent before converting
it to `EventConfig`, where empty fields represent no selection. Automatic directory
selection is an explicit host feature; the harness does not discover OS-specific
application directories or silently enable disk storage for SDK users.

The host prepares its application recording directory; the harness exclusively
creates the session archive. Creation failure is explicit, with no overwrite or
fallback. Automatic placement outside the project reduces accidental ingestion;
it is not isolation from arbitrary shell searches or intentional trace reads.
Memory remains the default. A bounded trace-inspection tool is separate future work.

## Effective configuration and execution provenance

```go
// package harness
type Provenance struct {
    WorkingDirectory string
    RecordingPath    string // Empty for memory or an opaque custom store.
    HarnessBuild     *BuildIdentity
    HostBuild        *BuildIdentity
}

type BuildIdentity struct {
    Module   string
    Version  string
    Revision string
    Modified *bool // Nil means unknown.
}
```

Extend the existing recorded effective configuration with provenance. Resolve the
working directory once to an absolute directory and pass that same value to
built-in tool construction and configuration. It is a construction-time execution
location, not a claim of filesystem isolation or immutable filesystem identity.

Hosts may supply build identities through construction dependencies. Available
build metadata can fill known values; absent information remains unknown. The
host executable's revision must never be labeled as the Strap dependency's
revision. The module field distinguishes those identities. Custom storage remains
opaque unless its host supplies a location description; no filesystem path is
inferred from an arbitrary store.

Configured model identity remains in effective configuration. Reported model
identity belongs to each provider completion, since the endpoint may change
during a session. Preserve the existing credential-exclusion policy for recorded
configuration; provenance does not introduce environment or credential dumps.

## Wire compatibility, projections, and inspection

Runtime types stay in domain packages. Wire DTOs have explicit snake_case tags;
the codec freezes new pointers, nested facts, metadata, and diagnostics. Inspection
returns independent copies. Framed large-record control payloads must preserve
new queryable fields so projection does not require materializing result bodies.

Extend shared projections and existing inspection paths with optional outcomes,
process facts, per-call usage breakdowns, completion metadata, and provenance.
SDK/HTTP inspection and replay expose the same values. Views may filter nonzero
exits separately from failed invocations without parsing embedded result JSON.

Older supported archives remain readable. Missing new fields remain unknown;
the absence of an old error field does not imply a known completed outcome.
New tool completion writers always include an outcome, although the wire field
remains optional for legacy compatibility. Starts have no outcome. A start with
no recorded completion remains incomplete, not failed, canceled, or completed.

These are optional extensions to existing event kinds. Keep the current schema
only if compatibility checks establish that old readers ignore additions safely
and existing meanings remain unchanged. Required semantic changes need a schema
increment. Do not rewrite old archives, cursors, hashes, or missing evidence.
Reader compatibility must not be confused with enforcing the stronger new-writer
contract; enforce that contract in publication and its checks.

## Implementation stages and acceptance evidence

1. **Tool outcomes and facts.** Add classification, retain partial evidence,
   derive one outcome, encode it, and expose it through shared inspection. Check
   ordinary nonzero exits, argument/unknown-tool rejection, execution failure,
   nested dependency deadlines, invocation timeout/cancellation, late cancellation
   after success, conflicting evidence, and partial output retained on errors.
   Confirm classification does not change model-visible conversion or continuation.
2. **Provider completion evidence.** Preserve metadata on every return path, add
   per-call usage breakdowns, and expose output completion metadata. Check `length`,
   malformed/interrupted streams, observer failure, absent/invalid counts, reported
   zero, subset semantics, cloning, and no metadata leakage into model history.
3. **Recording and provenance.** Add automatic archive directories and recorded
   resolved paths/build identities. Check empty explicitly selected destinations,
   conflicting selections before acquisition, directory-resolution failure, relative
   path semantics, exclusive creation, unchanged memory defaults, and unknown or
   distinct host/harness builds.

Each stage includes relevant legacy-archive, JSONL replay, and SDK/HTTP projection
checks, then its own commit. Include interrupted archives to prove no outcome is
manufactured. Required capture failure remains explicit, readers never drive
execution, and successful replay never executes recorded tools or model requests.

Raw HTTP archives, pre-completion crash metadata, automatic retries, new generation
budgets, automatic trace analysis, and a new logging service are outside this plan.
