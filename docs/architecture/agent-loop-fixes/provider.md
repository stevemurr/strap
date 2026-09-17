## 2–3. One completion validator and one output-failure contract

**Proposal only.** These are replacement/addition excerpts against the current
working tree, not an applied patch. Production and test files are unchanged.

I would make `provider.ValidateResponse` the sole implementation of generic
completion validity. The wire adapter and runtime both invoke that implementation;
the adapter retains only checks requiring wire metadata. I would also replace
`ToolArgumentsError` and the separate reasoning-limit sentinel with one typed
output-error contract. Runtime-generated `output_finished` facts become the source
for eval classification, including failures discovered *after* a provider returns.

### New `provider/output.go`; delete `provider/arguments.go`

This is the complete proposed central implementation (imports included). A
transport/parsing/storage failure remains infrastructure. A known output error
joined with a cancellation caused by the runtime's local abort remains an output
error. Actual caller cancellation takes precedence over output failure; a separate
infrastructure failure takes precedence over both. This avoids retrying after a
failed history/event write merely because another joined error is recoverable.

```go
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxToolCallIDBytes = 256
	MaxToolNameBytes   = 128
)

func ValidateToolName(name string) error {
	if strings.TrimSpace(name) == "" || !utf8.ValidString(name) || len(name) > MaxToolNameBytes {
		return fmt.Errorf("tool name must be nonblank UTF-8 of at most %d bytes", MaxToolNameBytes)
	}
	return nil
}

func ValidateToolIdentity(id, name string) error {
	if strings.TrimSpace(id) == "" || !utf8.ValidString(id) || len(id) > MaxToolCallIDBytes {
		return fmt.Errorf("tool call ID must be nonblank UTF-8 of at most %d bytes", MaxToolCallIDBytes)
	}
	return ValidateToolName(name)
}

type FailureClass string

const (
	FailureOutput         FailureClass = "output"
	FailureInfrastructure FailureClass = "infrastructure"
	FailureCanceled       FailureClass = "canceled"
)

type OutputErrorKind string

const (
	OutputArguments      OutputErrorKind = "arguments"
	OutputIdentity       OutputErrorKind = "call_identity"
	OutputEmpty          OutputErrorKind = "empty"
	OutputEncoding       OutputErrorKind = "encoding"
	OutputTokenLimit     OutputErrorKind = "token_limit"
	OutputReasoningLimit OutputErrorKind = "reasoning_limit"
	OutputToolCallLimit  OutputErrorKind = "tool_call_limit"
)

func (k OutputErrorKind) Valid() bool {
	switch k {
	case OutputArguments, OutputIdentity, OutputEmpty, OutputEncoding,
		OutputTokenLimit, OutputReasoningLimit, OutputToolCallLimit:
		return true
	default:
		return false
	}
}

// Diagnostic data is never an executable ToolCall. In particular, Arguments
// must remain a string: malformed JSON must be safe to persist as evidence.
type RejectedCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Index     int    `json:"index"`
	Calls     int    `json:"calls"`
}

type OutputError struct {
	Kind         OutputErrorKind `json:"kind"`
	Message      string          `json:"message,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Call         *RejectedCall   `json:"call,omitempty"`
}

func (e *OutputError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "rejected model output: " + string(e.Kind)
}

// Failure is the serializable interpretation of a complete generation failure.
// Output retains diagnostic evidence even if another joined cause makes the
// failure infrastructure-related or caller-canceled.
type Failure struct {
	Class  FailureClass `json:"class"`
	Output *OutputError `json:"output,omitempty"`
}

// Compact retains bounded outcome facts in a framed control record. The full
// Message, FinishReason and Call remain in the retained content record.
func (f Failure) Compact() Failure {
	if f.Output != nil {
		f.Output = &OutputError{Kind: f.Output.Kind}
	}
	return f
}

func ClassifyFailure(err, callerErr error) *Failure {
	if err == nil {
		return nil
	}
	var detail *OutputError
	infrastructure := false
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		switch e := err.(type) {
		case *OutputError:
			if !e.Kind.Valid() {
				infrastructure = true
				return
			}
			if detail == nil {
				copy := *e
				if e.Call != nil {
					call := *e.Call
					copy.Call = &call
				}
				detail = &copy
			}
		case interface{ Unwrap() []error }:
			for _, child := range e.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			if child := e.Unwrap(); child != nil {
				visit(child)
			} else {
				infrastructure = true
			}
		default:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				infrastructure = true
			}
		}
	}
	visit(err)
	f := &Failure{Output: detail}
	switch {
	case infrastructure:
		f.Class = FailureInfrastructure
	case callerErr != nil:
		f.Class = FailureCanceled
	case detail != nil:
		f.Class = FailureOutput
	default:
		f.Class = FailureInfrastructure
	}
	return f
}

// ValidateResponse validates the whole batch before history or tools accept it.
// It does not validate an operation's schema; that remains the tool contract.
func ValidateResponse(r Response) error {
	if !utf8.ValidString(r.Content) || !utf8.ValidString(r.Reasoning) {
		return &OutputError{Kind: OutputEncoding, Message: "model output is not valid UTF-8"}
	}
	seen := make(map[string]bool, len(r.ToolCalls))
	for i, call := range r.ToolCalls {
		reject := func(kind OutputErrorKind, message string) error {
			return &OutputError{Kind: kind, Message: message, Call: &RejectedCall{
				ID: call.ID, Name: call.Name, Arguments: string(call.Arguments),
				Index: i, Calls: len(r.ToolCalls),
			}}
		}
		if err := ValidateToolIdentity(call.ID, call.Name); err != nil {
			return reject(OutputIdentity, err.Error())
		}
		if seen[call.ID] {
			return reject(OutputIdentity, "duplicate tool call identity")
		}
		args := strings.TrimSpace(string(call.Arguments))
		if args == "" || !utf8.ValidString(args) || !json.Valid([]byte(args)) || args[0] != '{' {
			return reject(OutputArguments,
				fmt.Sprintf("tool %s arguments must be a complete JSON object (call %d of %d)", call.Name, i+1, len(r.ToolCalls)))
		}
		seen[call.ID] = true
	}
	if strings.TrimSpace(r.Content) == "" && len(r.ToolCalls) == 0 {
		return &OutputError{Kind: OutputEmpty, Message: "response contains no text or tool calls"}
	}
	return nil
}
```

`ClassifyFailure` leaves a nil error successful: cancellation arriving after
history commitment cannot retroactively turn that committed response into a
canceled output with a history position. The runtime joins/checks cancellation
before commitment. The classifier walks the entire joined error tree. A single `errors.As` check
would incorrectly retry `errors.Join(malformedOutput, eventWriteFailure)` and could
mislabel cancellation from the local reasoning cutoff. `callerErr` is the parent
generation context's error, **not** the child context canceled by `outputBuffer`.
An HTTP client's own deadline with a live parent is infrastructure, not user
cancellation. Unknown custom `OutputError.Kind` values are infrastructure errors;
only the finite recognized enum can enter a compact model-output diagnostic.

### Replace `decode` in `provider/internal/chatwire/wire.go`

Add the standard `errors` import. Remove the old identity/argument/nonempty
validation loop. This replacement parses wire data, calls the shared validator,
and records token exhaustion explicitly. Unsupported protocol metadata still
produces an infrastructure error. Usage is retained on every rejection.

```go
func decode(input completion) (provider.Response, error) {
	usage := decodeUsage(input.Usage)
	reject := func(err error) (provider.Response, error) {
		return provider.Response{Usage: usage}, err
	}
	if len(input.Choices) != 1 {
		return reject(fmt.Errorf("expected one choice, got %d", len(input.Choices)))
	}
	choice := input.Choices[0]
	if choice.Message.Role != "assistant" {
		return reject(fmt.Errorf("expected an assistant message"))
	}
	reasoning, err := reasoningText(choice.Message.Reasoning, choice.Message.ReasoningContent)
	if err != nil {
		return reject(err)
	}
	result := provider.Response{Usage: usage, Reasoning: reasoning}
	if choice.Message.Content != nil {
		result.Content = *choice.Message.Content
	}
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" {
			return reject(fmt.Errorf("unsupported tool call type %q", call.Type))
		}
		result.ToolCalls = append(result.ToolCalls, provider.ToolCall{
			ID: call.ID, Name: call.Function.Name,
			Arguments: json.RawMessage(call.Function.Arguments),
		})
	}
	validationErr := provider.ValidateResponse(result)
	var rejected *provider.OutputError
	if errors.As(validationErr, &rejected) {
		rejected.FinishReason = choice.FinishReason
	}
	switch choice.FinishReason {
	case "length":
		limit := &provider.OutputError{
			Kind: provider.OutputTokenLimit, FinishReason: choice.FinishReason,
			Message: "model response ended at the output token limit",
		}
		if rejected != nil {
			limit.Call = rejected.Call
		}
		return reject(limit)
	case "tool_calls":
		if len(result.ToolCalls) == 0 {
			return reject(fmt.Errorf("tool_calls finish without tool calls"))
		}
	case "stop":
	default:
		return reject(fmt.Errorf("unsupported finish reason %q", choice.FinishReason))
	}
	if validationErr != nil {
		return reject(validationErr)
	}
	return result, nil
}
```

`readStream` keeps its framing, index assembly, memory bounds, transport EOF,
role/type and `[DONE]` checks. Both assembled SSE and complete JSON responses
already end in `decode`, so they receive the same classification. Wire
observations remain visible as partial output; no rejected response enters model
history or executes a tool. This does not move stream parsing into the agent.

### Runtime acceptance: `agent/output.go`

The new identity bounds are shared with the bounded execution-header proposal.
In `agent.New`, replace `definition.Name == ""` with this, ensuring advertised
tool definitions obey the same name contract as generated calls:

```go
if err := provider.ValidateToolName(definition.Name); err != nil {
	return nil, fmt.Errorf("invalid tool: %w", err)
}
```

Replace the submit/finish pair in `generate` with:

```go
response, err := a.config.Spec.Provider.Submit(run, request, b)
if err == nil {
	err = provider.ValidateResponse(response)
}
bytes, reasoningBytes, flushErr := b.finish(response, err == nil && ctx.Err() == nil)
err = errors.Join(err, ctx.Err(), flushErr,
	a.recordUsage(revision, response.Usage), a.reportError())
```

Validation **must precede `b.finish`**. A provider that returns unstreamed text
with duplicate tool IDs must not have that text synthesized as accepted suffix
deltas before the batch is rejected. Already-published deltas remain evidence;
`finish(..., false)` still drains those observations, with no history commitment.

Inside the existing `control.mu` critical section, replace the duplicate empty
response check and its `else` with this (the surrounding stop/context guard stays):

```go
response.ToolCalls = provider.CopyCalls(response.ToolCalls)
pos := a.thread.append(provider.Message{
	Role: "assistant", Content: content.Text(response.Content),
	ToolCalls: response.ToolCalls,
})
position = &pos
```

Replace the trailing cancellation classification and terminal publication with:

```go
callerErr := ctx.Err()
if callerErr == nil && a.stopRequested.Load() {
	callerErr = context.Canceled
}
failure := provider.ClassifyFailure(err, callerErr)
if failure != nil && failure.Class == provider.FailureCanceled {
	status = OutputCanceled
}
finishErr := a.report(OutputFinished{
	Output: id, Status: status, Bytes: bytes, ReasoningBytes: reasoningBytes,
	HistoryPosition: position, Err: err, Failure: failure,
	FinishedAt: time.Now().UTC(),
})
return response, id, errors.Join(err, finishErr)
```

The existing history-publication error return remains unchanged: do not invent a
successful terminal event after history publication fails. Remove the now-unused
`strings` import from this file.

Replace the reasoning limit rejection in `outputBuffer.OnDelta`:

```go
return reject(&provider.OutputError{
	Kind: provider.OutputReasoningLimit,
	Message: "reasoning limit exceeded",
})
```

Delete `agent.ErrReasoningLimit`; update its references to the shared typed kind.
Stream-prefix disagreement and event-write failure remain infrastructure errors:
they are adapter/publication contract failures, not malformed model tool calls.

### Recovery: `agent/agent.go`

Replace both `errors.As(ToolArgumentsError)` / `errors.Is(ErrReasoningLimit)` retry
branches with the following. The existing `malformed`, `overrun`, limits, and
reset-after-success behavior remain. Remove the redundant empty response check
inside the `len(response.ToolCalls) == 0` final-reply branch.

```go
failure := provider.ClassifyFailure(err, ctx.Err())
if failure != nil && failure.Class == provider.FailureOutput && !a.stopRequested.Load() {
	notice := ""
	switch failure.Output.Kind {
	case provider.OutputArguments:
		if malformed < maxMalformedCalls {
			malformed++
			notice = fmt.Sprintf("Your previous response was discarded and no tool in it ran: %v. Emit complete JSON objects for every tool's arguments.", failure.Output)
		}
	case provider.OutputReasoningLimit:
		if overrun < maxReasoningRetries {
			overrun++
			notice = fmt.Sprintf("Your previous response was cut off after %d KB of reasoning, and no tool in it ran. Emit the next tool call or final reply directly.", a.config.Spec.ReasoningLimit>>10)
		}
	}
	if notice != "" {
		if _, appendErr := a.appendHistory(provider.Message{
			Role: "user", Content: content.Text(notice),
		}, nil); appendErr != nil {
			return appendErr
		}
		continue
	}
}
if err != nil {
	return err
}
```

Token-limit output is **scorable but terminal** under this proposal. It does not
automatically reuse the same cap and burn another request. Regeneration remains
limited to the existing malformed-arguments and reasoning-overrun policies. It
occurs before any accepted tool batch; no tool failure or ambiguous effect is
automatically retried.

### Record the classification once; migrate its existing consumer

Add to `agent.OutputFinished` and `record.OutputFinished` respectively:

```go
Failure *provider.Failure
```

```go
Failure *provider.Failure `json:"failure,omitempty"`
```

Delete `record.OutputFinished.RejectedToolCall`. In
`harness/eventcodec/agent.go`, copy `Failure: e.Failure` into the record and replace
the existing code-selection / `ToolArgumentsError` block with:

```go
if e.Err != nil {
	if e.Failure == nil {
		return eventlog.Data{}, nil, errors.New("output error lacks failure classification")
	}
	p.Error = &eventlog.Problem{
		Code: string(e.Failure.Class), Message: e.Err.Error(),
	}
}
```

On decode, copy `Failure: v.Failure` into `agent.OutputFinished` and replace the
old rejected-tool reconstruction with:

```go
if v.Error != nil {
	f.Err = errors.New(v.Error.Message)
	if v.Failure != nil && v.Failure.Output != nil {
		f.Err = recordedOutputError{error: f.Err, rejected: v.Failure.Output}
	}
}
```

Change `recordedOutputError.rejected` to `*provider.OutputError`; its `Unwrap`
method stays. This preserves structured diagnostics and original joined error
text through replay. Consumers read `Failure.Class` from the recorded fact; they
do not reclassify a flattened recorded error string.

In `harness/eventcodec/publish.go`'s `agent.OutputFinished` control-record case,
copy the compact failure before calling `describeAgent`:

```go
if y.Failure != nil {
	failure := y.Failure.Compact()
	y.Failure = &failure
}
```

This is required when malformed arguments make the full terminal fact large.
The control record retains class/kind while the full raw arguments remain in
content. Also tighten `harness/projection/projector.go`'s existing
`output_finished` checks: a completion must have nil `Failure`; a failure must
have a recognized nonempty class; `OutputCanceled` must match `FailureCanceled`;
`FailureOutput` must include a nonnil output error. These are record-integrity
checks, not a second output-error classifier. Existing synthetic event fixtures
must populate `Failure` explicitly using `ClassifyFailure`.

### Evals classify runtime facts, not only provider return values

In `eval/interaction/runner.go`, remove `requestGate.providerError` and
`requestGate.outputErrors`, and their type/sentinel-based classification block.
Change `stats` to return only `(calls int, budget bool)` and migrate its three
call sites. Keep `calls` and tool-budget accounting. Immediately after the inner
provider returns, invoke the **same** validator before budget accounting or the
race-injection callback:

```go
if err == nil {
	err = provider.ValidateResponse(r)
}
```

The gate's existing pre-dispatch budget error becomes:

```go
return provider.Response{Usage: r.Usage}, &provider.OutputError{
	Kind: provider.OutputToolCallLimit,
	Message: "interaction tool-call budget exceeded before dispatch",
}
```

This keeps the declared tool-call budget a scorable model-output limit. It also
prevents a malformed response from triggering the race-injection callback or
being misreported as a budget overrun.

Add the following to `eval/interaction/runner.go` and call it after reading the
final pre-cleanup prefix, before `grade`:

```go
func applyOutputOutcomes(result *Result, actor identity.ActorID, facts []fact) {
	result.Behavior.OutputErrors = 0
	for _, x := range facts {
		if x.record.Sequence <= result.Start.Sequence || x.record.Sequence > result.Through.Sequence {
			continue
		}
		e, ok := x.event.(conversation.AgentEvent)
		if !ok || e.Agent != actor {
			continue
		}
		out, ok := e.Event.(agent.OutputFinished)
		if !ok || out.Failure == nil {
			continue
		}
		switch out.Failure.Class {
		case provider.FailureOutput:
			result.Behavior.OutputErrors++
		case provider.FailureInfrastructure:
			if result.Error == "" {
				result.ErrorClass = "provider"
				result.Error = fmt.Sprint(out.Err)
			}
		case provider.FailureCanceled:
			// A declared eval deadline is graded as budget exhaustion below.
			if result.Error == "" && result.StopReason != "timeout" {
				result.ErrorClass = "interrupted"
				result.Error = fmt.Sprint(out.Err)
			}
		}
	}
}
```

Replace the post-loop `gate.stats` / provider-error assignment with:

```go
var toolBudget bool
result.ModelCalls, toolBudget = gate.stats()
if toolBudget {
	result.StopReason = "tool_budget"
}
applyOutputOutcomes(&result, f.actor(), facts)
```

Delete `failedOutputs` and its `OutputFailed` counting branch and final `max`
assignment in **both** `eval/interaction/oracle.go` and
`eval/interaction/schema_oracle.go`. Retain unrelated `AgentEvent` handling.
That avoids three separate guesses at the same count. Pure-oracle unit helpers
should invoke `applyOutputOutcomes` before `grade`, as the runner does.

An empty/duplicate response from a direct provider is now counted even though its
`Submit` returned nil error. A length-limited adapter response stays in the model
denominator and increments `OutputErrors`; a HTTP/EOF/publication failure is
excluded. No error-string matching is involved.

### Exact migration/check scope

The snippets above are **not** presented as a complete applicable diff. Required
remaining mechanical edits are imports, removal of the old sentinel/type, and
fixture/expectation migration in:

- `agent/malformed_test.go`, `agent/reasoning_limit_test.go`;
- `provider/arguments_test.go` (rename to `output_test.go`),
  `provider/internal/chatwire/arguments_test.go`, `finish_test.go`;
- `harness/eventcodec/arguments_test.go`, `codec_test.go`,
  `harness/rejected_arguments_test.go`, `harness/projection/integrity_test.go`;
- `eval/interaction/adversarial_test.go`, `runner_test.go`,
  `oracle_test.go`, `schema_oracle_test.go` and any helper constructing an
  `OutputFinished` failure;
- comments referencing `ErrReasoningLimit` in `agent/agent.go`, and
  `eval/interaction/README.md`'s currently injected malformed-output test note.

No compatibility alias for `ToolArgumentsError` or `ErrReasoningLimit`, and no
second legacy decoder/classifier. The output and tool-record changes share one schema bump to version 6.
`SupportedSchema` accepts only version 6; old archives are rejected explicitly.
There is no legacy decoder or automatic archive migration.

Regression tests I would add before implementation:

1. Table-driven joined-error classification: malformed + local cancellation;
   malformed + actual caller cancellation; malformed + event-write failure;
   wrapped errors and joined order reversal; HTTP-client timeout with a live
   parent versus trial/parent timeout. Infrastructure failure must never trigger
   regeneration.
2. Identical accepted/rejected completion corpus through actual JSON/SSE adapter
   fixtures and a direct provider: duplicate/blank IDs, blank names, invalid or
   non-object arguments, empty output, valid text and multi-call output.
3. A rejected whole batch has **zero** tool invocations and no assistant history;
   streamed evidence remains, but unobserved final suffixes are not published.
4. Actual length-terminated JSON and SSE fixtures through the interaction runner:
   scorable=true, outputErrors=1, no dispatch, no implicit token-limit retry.
   HTTP failure, broken stream and external cancellation remain excluded.
5. Malformed/reasoning recovery still works within the existing bounds; the
   original failure remains in the denominator after recovery.
6. Raw malformed argument diagnostics survive encode/decode and large-record
   framing; compact records retain failure class/kind and replay still resolves
   the original diagnostic string.

These are proposed tests; this proposal has not been compiled or run.
