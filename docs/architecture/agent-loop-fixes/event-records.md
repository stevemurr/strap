# Preferred event fix: one bounded tool execution header

Proposal only; no source files changed and this broader migration has not been compiled. `Reader.At` and list endpoints must remain able to index tool metadata without loading arbitrarily large arguments/results.

## 1. Store one header in both representations

Add `harness/record/tool.go`. The inline body embeds this header, and the framed control is exactly the same header value. Both projector and inspection use `ReadToolHeader`. Delete the private `toolActivityWire`/`toolRecord` shapes and both independent anonymous framed-tool parsers.

```go
const (
    MaxToolHeaderBytes = 16 << 10
    MaxToolErrorSummaryBytes = 256
)

type ToolState string
const (
    ToolStarted ToolState = "started"
    ToolSucceeded ToolState = "succeeded"
    ToolFailed ToolState = "failed"
    ToolCanceled ToolState = "canceled"
)

type ToolHeader struct {
    Agent identity.ActorID `json:"agent"`
    InvocationID identity.ToolInvocationID `json:"invocation_id"`
    Output *identity.OutputID `json:"output,omitempty"`
    ProviderCallID string `json:"provider_call_id"`
    Name string `json:"name"`
    StartedAt time.Time `json:"started_at"`
    FinishedAt time.Time `json:"finished_at,omitempty"`
    State ToolState `json:"state"`
    Error *eventlog.Problem `json:"error,omitempty"`
    Execution *tool.ExecutionBinding `json:"execution,omitempty"`
}

type ToolActivity struct {
    ToolHeader
    Arguments string `json:"arguments"` // Preserve malformed arguments as evidence.
    Content content.Content `json:"content,omitempty"`
    Captured content.Content `json:"captured,omitempty"`
    ErrorDetail string `json:"error_detail,omitempty"`
    Diagnostic *tool.Diagnostic `json:"diagnostic,omitempty"`
}

func boundText(name, value string, maximum int) error {
    if !utf8.ValidString(value) || len(value) > maximum {
        return fmt.Errorf("%s exceeds its UTF-8 byte contract", name)
    }
    return nil
}

func toolErrorSummary(s string) string {
    s = strings.ToValidUTF8(s, "\uFFFD")
    if len(s) <= MaxToolErrorSummaryBytes { return s }
    n := MaxToolErrorSummaryBytes - len("...")
    for n > 0 && !utf8.RuneStart(s[n]) { n-- }
    return s[:n] + "..."
}

func (h ToolHeader) Validate() error {
    if h.Agent == "" || h.InvocationID == "" || h.StartedAt.IsZero() {
        return errors.New("incomplete tool identity or start time")
    }
    if err := boundText("agent", string(h.Agent), 256); err != nil { return err }
    if err := boundText("invocation", string(h.InvocationID), 256); err != nil { return err }
    if err := provider.ValidateToolIdentity(h.ProviderCallID, h.Name); err != nil { return err }
    if h.Output != nil {
        if h.Output.Agent != h.Agent || h.Output.Call == 0 {
            return errors.New("invalid tool output identity")
        }
    }
    if err := tool.ValidateExecutionBinding(h.Execution); err != nil { return err }
    if h.Execution != nil && h.Execution.Actor != h.Agent {
        return errors.New("tool execution actor mismatch")
    }
    switch h.State {
    case ToolStarted:
        if !h.FinishedAt.IsZero() || h.Error != nil || h.Execution != nil {
            return errors.New("invalid tool start")
        }
    case ToolSucceeded:
        if h.FinishedAt.IsZero() || h.Error != nil { return errors.New("invalid tool success") }
    case ToolFailed, ToolCanceled:
        if h.FinishedAt.IsZero() || h.Error == nil { return errors.New("invalid tool failure") }
        switch h.Error.Code {
        case "tool_error", "timeout", "canceled":
        default: return errors.New("invalid tool failure code")
        }
        if (h.State == ToolCanceled) != (h.Error.Code == "canceled") {
            return errors.New("tool failure state/code mismatch")
        }
        if err := boundText("error summary", h.Error.Message, MaxToolErrorSummaryBytes); err != nil { return err }
    default:
        return errors.New("invalid tool state")
    }
    raw, err := json.Marshal(h)
    if err != nil { return err }
    if len(raw) > MaxToolHeaderBytes { return errors.New("tool header exceeds byte budget") }
    return nil
}

func DescribeTool(actor identity.ActorID, a agent.ToolActivity) (ToolActivity, error) {
    if errors.Is(a.Err, tool.ErrExecutionBinding) { return ToolActivity{}, a.Err }
    h := ToolHeader{
        Agent: actor, InvocationID: identity.ToolInvocationID(a.InvocationID),
        Output: a.Output, ProviderCallID: a.Call.ID, Name: a.Call.Name,
        StartedAt: a.StartedAt, FinishedAt: a.FinishedAt,
        State: ToolStarted, Execution: a.Result.Execution,
    }
    detail := ""
    if !a.FinishedAt.IsZero() {
        h.State = ToolSucceeded
        if a.Err != nil {
            detail = a.Err.Error()
            h.State = ToolFailed
            code := "tool_error"
            if errors.Is(a.Err, context.DeadlineExceeded) { code = "timeout" }
            if errors.Is(a.Err, context.Canceled) { h.State, code = ToolCanceled, "canceled" }
            h.Error = &eventlog.Problem{Code: code, Message: toolErrorSummary(detail)}
        }
    } else if a.Err != nil {
        return ToolActivity{}, errors.New("unfinished tool contains terminal error")
    }
    if err := h.Validate(); err != nil { return ToolActivity{}, err }
    return ToolActivity{ToolHeader: h, Arguments: string(a.Call.Arguments),
        Content: a.Result.Content, Captured: a.Result.Captured,
        ErrorDetail: detail, Diagnostic: a.Diagnostic}, nil
}

func ReadToolHeader(e eventlog.Record) (ToolHeader, error) {
    raw := e.Payload
    if frame, ok := Frame(raw); ok {
        raw = frame.Control
        if len(raw) > MaxToolHeaderBytes {
            return ToolHeader{}, errors.New("framed tool header exceeds byte budget")
        }
    }
    var h ToolHeader
    if err := json.Unmarshal(raw, &h); err != nil { return h, err }
    if err := h.Validate(); err != nil { return h, err }
    if h.Agent != identity.ActorID(e.Agent) || string(h.InvocationID) != e.Correlation {
        return h, errors.New("tool envelope/header identity mismatch")
    }
    if (h.Output == nil) != (e.Output == nil) || h.Output != nil && *h.Output != *e.Output {
        return h, errors.New("tool envelope/header output mismatch")
    }
    return h, nil
}
```

`DescribeTool` is the **only** conversion of a runtime outcome into a persisted tool summary. Its full body carries unbounded error detail, arguments, and result content. The header contains only a 256-byte UTF-8 error summary. The byte budget measures actual JSON after escaping, not merely Go string lengths.

A failed/canceled tool retains an explicit terminal state even if its full error text is empty or huge. A started-but-unfinished tool is represented as `started`; do not manufacture a finish or infer whether its partial side effects occurred.

## 2. Carry output correlation directly from the runtime

In `agent/tools.go` add the output to the notification, and clone it in `reportTool`:

```go
type ToolActivity struct {
    Output *identity.OutputID `json:"output,omitempty"`
    // Existing InvocationID, Diagnostic, Call, StartedAt, FinishedAt, Result, Err.
}

// In reportTool before calling observers:
if activity.Output != nil {
    output := *activity.Output
    activity.Output = &output
}
```

In the model-response loop, change the invocation to:

```go
result, err := a.invokeCall(ctx, &output, call, rejected)
```

Change the helper signature and its two event literals to carry the same output:

```go
func (a *Agent) call(ctx context.Context, call provider.ToolCall) (tool.Result, error) {
    return a.invokeCall(ctx, nil, call, nil)
}

func (a *Agent) invokeCall(ctx context.Context, output *identity.OutputID,
    call provider.ToolCall, rejected error) (result tool.Result, err error) {
    // Existing implementation. Add Output: output to both ToolActivity literals.
}
```

The source-level `call` helper is for direct host/test calls and has no originating model output. Every model dispatch has one. `Output` no longer depends on reconstructing a map from assistant history or on storing an unbounded list of call IDs in history control.

## 3. Enforce finite identity/binding contracts centrally

The provider proposal defines these shared constants/helpers:

```go
const MaxToolCallIDBytes = 256
const MaxToolNameBytes = 128
func ValidateToolIdentity(id, name string) error
func ValidateToolName(name string) error
```

They reject non-UTF-8, blank, and over-limit values. `ValidateResponse` invokes the first **before history commitment or dispatch**. `agent.New` invokes `ValidateToolName` for every registered tool, including injected implementations. The record validator reuses these same functions; it does not maintain another name/ID policy.

Add this helper to `tool/execution_evidence.go` (tool already depends on identity):

```go
const MaxExecutionIdentityBytes = 256
var ErrExecutionBinding = errors.New("invalid execution binding")

func ValidateExecutionBinding(b *ExecutionBinding) error {
    if b == nil { return nil }
    for name, value := range map[string]string{
        "evidence_ref": b.EvidenceRef, "work_id": b.WorkID, "actor": string(b.Actor),
    } {
        if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > MaxExecutionIdentityBytes {
            return fmt.Errorf("%w: %s", ErrExecutionBinding, name)
        }
    }
    if b.AssignedAtRevision == 0 { return fmt.Errorf("%w: assignment revision is zero", ErrExecutionBinding) }
    return nil
}
```

Replace the opening of `tool.ExecutionResult` with this exact prefix, retaining the existing receipt encoding after it:

```go
func ExecutionResult(r Result, b *ExecutionBinding, cause error) (Result, error) {
    r.Execution = b
    r.Captured = r.Content.Clone()
    if b == nil {
        return r, errors.Join(cause, fmt.Errorf("%w: binding is required", ErrExecutionBinding))
    }
    if err := ValidateExecutionBinding(b); err != nil {
        return r, errors.Join(cause, err)
    }
    // Existing receipt encoding starts with: var raw json.RawMessage
}
```

The nil binding is legal for an ordinary tool header but illegal for `ExecutionResult`, which requires execution attribution and dereferences the binding. The explicit guard prevents that dereference. Invalid attribution and captured content remain in the returned result, and the typed `ErrExecutionBinding` marker forces `DescribeTool` to return a capture error rather than classify this as an ordinary tool failure.

Also invoke `ValidateExecutionBinding(binding)` immediately after constructing `binding` in `internal/workflow/research_diagnostic.go`, **before** `s.researchShell.Call`, returning its typed error on failure. The record validator also invokes it for all tools, including a custom tool that supplies its own execution binding. A custom tool returning an invalid binding after effects have occurred is a host contract/capture failure: latch capture health, stop execution, preserve previously accepted facts, and never retry that tool automatically. Do not misclassify it as model output rejection.

## 4. Publication uses the already-produced header

In `eventcodec.describe` replace its `conversation.ToolEvent` branch:

```go
case conversation.ToolEvent:
    body, err := record.DescribeTool(v.Agent, v.Activity)
    if err != nil { return eventlog.Data{}, nil, err }
    return eventlog.Data{
        Kind: "tool", Agent: string(v.Agent),
        Correlation: v.Activity.InvocationID, Output: body.Output,
    }, body, nil
```

Change `control` to accept the already-described body, `control(e conversation.Event, body any)`. The sole tool branch becomes:

```go
case conversation.ToolEvent:
    activity, ok := body.(record.ToolActivity)
    if !ok { return nil, errors.New("tool payload has unexpected type") }
    v = activity.ToolHeader
```

Update `Publish`'s metadata closure to `control(e, body)`. No second call to `DescribeTool`, no independently-maintained control fields. `EncodeEvent` uses the same `describe` path. Validate the header for **all** payload sizes before encoding/framing starts. A large payload must not be the first time invalid metadata is discovered.

Explicitly validate the queue budget at session construction. `MaxToolHeaderBytes` is 16 KiB; use a 32 KiB minimum event publication queue, leaving space for the framed reference and envelope. Reject smaller configurations before any agent starts. The default is already 8 MiB. Update tests that currently select 4 KiB to the new minimum; force framing via larger bodies instead. Also retain actual final record-size/queue checks, since runtime metadata limits do not replace storage budget validation. `NewPublisher` should return `(*Publisher, error)` and reject `queueBytes < MinPublisherQueueBytes`, where `MinPublisherQueueBytes = 32 << 10`; its call sites handle this construction error.

The publisher constructor becomes:

```go
const MinPublisherQueueBytes = 32 << 10

func NewPublisher(log *eventlog.Log, queueBytes int) (*Publisher, error) {
    if queueBytes < MinPublisherQueueBytes {
        return nil, fmt.Errorf("event publication queue must be at least %d bytes", MinPublisherQueueBytes)
    }
    return &Publisher{
        log: log,
        chunkBytes: min(64<<10, (queueBytes-2048)/2),
        producers: make(chan struct{}, 64),
        deltaBytes: (min(queueBytes, 64<<10) - 1024) / 6,
    }, nil
}
```

In `harness.New`, replace its single constructor call with:

```go
s.encoder, err = eventcodec.NewPublisher(s.log, s.config.Events.Queue.Bytes)
if err != nil {
    return nil, err
}
```

On `describe`/header validation failure, keep `Session.publish` as the existing owner of capture failure. Its current encoder-error branch already calls `s.log.Fail(err)`; change the return to `errors.Join(eventlog.ErrCapture, err)`. Do not add a second failure owner inside `Publisher`. The start event is published before tool effects; the finish event may expose a custom tool's invalid result after effects, hence the deliberate terminal capture behavior. `ErrExecutionBinding` is deliberately propagated through `DescribeTool` as a capture failure instead of converted to an ordinary tool-error header.

## 5. Both consumers read the same header

Replace the entire initial inline/framed `case "tool"` extraction in `harness/projection/projector.go` with:

```go
h, err := record.ReadToolHeader(e)
if err != nil { return err }
execution := h.Execution
invocation := string(h.InvocationID)
started, finished := h.StartedAt, h.FinishedAt
```

**Important call-site detail:** `Projector.Apply` currently substitutes `e.Payload = framed.Control` before the switch. Retain `originalRecord := e` before that substitution and pass `record.ReadToolHeader(originalRecord)` here. The helper handles framing itself and checks the same envelope in both representations. Keep existing start/finish, execution-attribution, and duplicate-evidence transition validation after this extraction. Add output-existence/agent matching to the tool transition check when `h.Output != nil`.

Replace the initial extraction in inspection's `case "tool"` with:

```go
h, err := record.ReadToolHeader(e)
if err != nil { return err }
t := ToolView{
    Execution: h.Execution, InvocationID: h.InvocationID, Agent: h.Agent,
    Output: h.Output, ProviderCallID: h.ProviderCallID, Name: h.Name,
    State: h.State, Error: h.Error,
}
if !h.StartedAt.IsZero() { value := h.StartedAt; t.StartedAt = &value }
if !h.FinishedAt.IsZero() { value := h.FinishedAt; t.FinishedAt = &value }
```

Add `State record.ToolState json:"state"` to `ToolView`. Keep existing cursor/order/evidence indexing and cloning. Delete `callOutputs` from `viewIndex`, its initialization, the `history_appended` indexing branch, and the callOutputs lookup from the tool branch. The projector still indexes history using its existing bounded position/role/output control; inspection no longer needs to read tool calls from its body.

The API now consistently returns a bounded summary error for inline and framed results. The full retained error is accessed by explicitly resolving the finish record, just like full tool output. That distinction must be documented on `ToolView.Error`.

## 6. Decode and format migration

The tool payload shape changes, so bump `eventlog.SchemaVersion` from 5 to 6. The output and tool changes share this one format change. Update `eventcodec.decodeTool` to decode `record.ToolActivity`, validate its header, and reconstruct `agent.ToolActivity` with `Output`, `provider.ToolCall`, timestamps, `tool.Result{Content, Captured, Execution}`, and diagnostic. Restore `Err` from `ErrorDetail` when header.Error is non-nil, even when detail is empty. For canceled/timeout use a small recorded-error wrapper whose `Error()` returns the original detail and whose `Unwrap()` returns `context.Canceled` / `context.DeadlineExceeded`; re-encoding therefore preserves typed status. For ordinary tool failure it unwraps nil. Do not infer cancellation by matching a string.

Replace `harness/eventcodec/tools.go`'s old wire structs and decoder with the shared record type and this decoder (adjust imports to include `context` and `harness/record`, removing the old `message`, `time`, and `errors` imports):

```go
type recordedToolError struct {
    detail string
    cause error
}

func (e recordedToolError) Error() string { return e.detail }
func (e recordedToolError) Unwrap() error { return e.cause }

func decodeTool(raw []byte) (conversation.Event, error) {
    var body record.ToolActivity
    if err := json.Unmarshal(raw, &body); err != nil {
        return nil, err
    }
    if err := body.ToolHeader.Validate(); err != nil {
        return nil, err
    }
    activity := agent.ToolActivity{
        Output: body.Output,
        InvocationID: string(body.InvocationID),
        Call: provider.ToolCall{
            ID: body.ProviderCallID, Name: body.Name,
            Arguments: json.RawMessage(body.Arguments),
        },
        StartedAt: body.StartedAt, FinishedAt: body.FinishedAt,
        Result: tool.Result{
            Content: body.Content, Captured: body.Captured,
            Execution: body.Execution,
        },
        Diagnostic: body.Diagnostic,
    }
    if body.Error != nil {
        var cause error
        switch body.Error.Code {
        case "canceled":
            cause = context.Canceled
        case "timeout":
            cause = context.DeadlineExceeded
        }
        activity.Err = recordedToolError{detail: body.ErrorDetail, cause: cause}
    }
    return conversation.ToolEvent{Agent: body.Agent, Activity: activity}, nil
}
```

Choose a clean format break, with no dual decoder:

```go
const SchemaVersion = 6
func SupportedSchema(schema int) bool { return schema == SchemaVersion }
```

Schema 2–5 archives now return the existing unsupported-schema error. Update legacy fixtures and documentation to assert that behavior. No offline migration is promised by this proposal. The separate provider proposal's `record.OutputFinished.Failure *provider.Failure` change shares this schema-6 bump; it replaces the old `RejectedToolCall` diagnostic. Its framed output-control code retains the bounded failure class/kind and keeps rejected arguments in full content. Tool headers and output headers remain different domain records, each with one authoritative encoder and one representation-independent reader.

There is one header definition, one runtime-to-header converter, one validator, and one header reader shared by publisher, projector, and inspection. No new dependency cycle: record already depends on agent/tool/provider; those packages do not import harness/record.

## Regression seams

- Same tool start/result/error at sizes immediately below/above framing threshold: exact identity, output, timestamps, explicit state, error category, and bounded error summary match.
- Large assistant arguments do not affect output correlation; inspection must not resolve history/tool content to list tools.
- Long valid IDs and worst-case JSON escaping fit header/queue budgets; invalid model IDs are rejected before assistant commitment and invocation; invalid registered names fail construction.
- Oversized or malformed custom execution bindings fail capture, preserve prior evidence, and cause zero retries. Built-in diagnostic binding is validated before shell execution.
- Invalid header rejected identically before framing at both sizes; wrong header/envelope agent/output/correlation rejected by both consumers.
- Huge/empty error text, cancellation, timeout, interrupted unfinished tool, and partial failed output retain explicit state; resolving the full finish record returns original error detail.
- Inspect/list/HTTP/archive projection agree, use bounded reads, and preserve all current duplicate-start/finish/evidence checks.
- Schema-2–5 archive rejection is explicit under the clean break; schema-6 round trip preserves failure category and full error detail.
