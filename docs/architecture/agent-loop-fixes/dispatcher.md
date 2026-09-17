# Required workflow dispatcher: proposed patch (not applied)

The dispatcher owns a terminal result. The harness observes that result and uses
its existing closing lifecycle, admission gate, execution cancellation, and
outcome. There is no second health registry or automatic restart. A read/decode
failure is an execution failure; it is not a resource-cleanup failure and must not
poison the retained event log.

## `internal/workflow/session.go`: retain the terminal cause

Add one field to `Session`:

```go
// Written before done closes; read only after observing done.
runErr error
```

Add these methods (Done now explicitly means the workflow lifetime, rather than
the embedded controller lifetime):

```go
func (s *Session) Done() <-chan struct{} { return s.done }

// Err reports the terminal dispatcher failure. It is nil while running and
// after orderly shutdown. Done distinguishes those states.
func (s *Session) Err() error {
    select {
    case <-s.done:
        return s.runErr
   default:
        return nil
    }
}

func (s *Session) readerOutcome(err error) error {
    if errors.Is(err, inbox.ErrClosed) {
        if s.closing.Load() {
            return nil
        }
        select {
        case <-s.Controller.Done():
            return nil
       default:
        }
    }
    if errors.Is(err, context.Canceled) && s.closing.Load() {
        return nil
    }
    return fmt.Errorf("workflow dispatcher: read event: %w", err)
}
```

Replace the `run()` preamble through `defer func() { s.cancel(); <-readerDone }()`
with the following. The existing dispatch body follows inside `dispatch`.

```go
func (s *Session) run() {
    s.runErr = s.dispatch()
    s.BeginClosing()
    if s.events != nil {
        s.events.Close()
    }
    if s.stopOwner != nil {
        s.stopOwner()
    }
    close(s.done)
}

func (s *Session) dispatch() error {
    type eventRead struct {
        event conversation.Event
        err   error
    }
    incoming := make(chan eventRead)
    readerDone := make(chan struct{})
    go func() {
        defer close(readerDone)
        for {
            e, err := s.Controller.NextEvent(s.ctx)
            select {
            case incoming <- eventRead{event: e, err: err}:
            case <-s.ctx.Done():
                return
            }
            if err != nil {
                return
            }
        }
    }()
    defer func() { s.cancel(); <-readerDone }()

    // Existing delivered/failed/pending maps, notice queue, timer, and drain
    // function remain here unchanged.
```

Within the existing dispatch select, replace the context cancellation arm:

```go
case <-s.ctx.Done():
    return s.readerOutcome(s.ctx.Err())
```

Replace `case e, ok := <-incoming` and its `if !ok` block with:

```go
case next := <-incoming:
    if next.err != nil {
        // Classify BEFORE setting closing, otherwise an unexpected EOF would
        // be incorrectly converted into an orderly shutdown.
        err := s.readerOutcome(next.err)
        s.BeginClosing() // Fences shared mutation admission immediately.
        drain()          // Retains accepted legacy events; closing prevents delivery.
        return err
    }
    e := next.event
    // Existing relay and event switch remain unchanged.
```

Delete the old deferred closure of `incoming`: the reader now delivers its final
error explicitly instead of representing every error as an empty stream.

Keep `Close` as an ownership/join operation. Its successful return remains nil,
even when `Err()` reports failed dispatch, and clarify the method comment:

```go
// Close seals admission and joins the controller and dispatcher. Its error
// describes failure to finish this wait, not the dispatcher's execution result;
// that result is available from Err after Done closes.
```

This distinction matters: returning `runErr` from `Close` would make the harness
skip resource cleanup and retry an immutable execution error forever.

`operations.go` does not need another independently maintained health check.
`BeginClosing` already sets the private closing flag and seals the shared gate;
the changed error path now calls it instead of only setting the private flag.

## `internal/workflow/interrupt.go`: do not hide a terminal failure

Both `case <-s.done` arms in `SettleInterrupt` become:

```go
case <-s.done:
    if err := s.Err(); err != nil {
        return err
    }
    return conversation.ErrClosed
```

Add the conversation import. The current `return s.ctx.Err()` loses the original
read/decode failure behind the cleanup cancellation.

## `harness/session.go`: arm supervision after assembly

Change `startupError string` to `startupError error`, and the constructor rollback
assignment from `s.startupError = err.Error()` to `s.startupError = err`. This retains
causal identity so reporting can join startup and dispatch errors without
repeating the same cause.

At the end of New, after `PublishConfiguration` succeeds, retain installation
of the owner cancellation callback under `s.mu` and then insert the following
immediately before `return s, nil`:

```go
select {
case <-s.workflow.Done():
    if err = s.workflow.Err(); err == nil {
        err = errors.New("workflow stopped during session startup")
    }
    return nil, err
default:
}

go s.superviseWorkflow()
```

All controller, workflow, telemetry, configuration, and resources have been
installed at this point. A constructor-time failure is handled by the existing
rollback defer; it cannot race a supervisor closing half-built resources.
If failure occurs between the select and the goroutine running, the workflow
has already sealed admission and its closed Done channel preserves notification.

Add to `harness/lifecycle.go`:

```go
func (s *Session) superviseWorkflow() {
    <-s.workflow.Done()
    if s.workflow.Err() != nil {
        // This starts the existing asynchronous finalizer. It does not wait
        // inside the dispatcher or reenter event publication.
        s.startCloseReason("workflow_failed")
    }
}
```

## `harness/lifecycle.go`: execution outcome is separate from cleanup status

Add `errors` to imports and this helper (caller holds `s.mu`):

```go
func (s *Session) refreshExecutionOutcomeLocked() {
    failure := errors.Join(s.executionError, s.startupError)
    if s.workflow != nil {
        if err := s.workflow.Err(); err != nil {
            if !errors.Is(failure, err) {
                failure = errors.Join(failure, err)
            }
            if s.startupError == nil {
                s.outcome.Reason = "workflow_failed"
            }
        }
    }
    if s.startupError != nil {
        s.outcome.Reason = "startup_failed"
    }
    s.outcome.Error = ""
    if failure != nil {
        s.outcome.Error = eventlog.Summary(failure.Error())
    }
}
```

In `startCloseReason`, after `s.mu.Lock()`/`defer s.mu.Unlock()` but before returning
an existing attempt, refresh an existing outcome:

```go
if s.outcome != nil {
    s.refreshExecutionOutcomeLocked()
}
```

Remove the old `if s.startupError != "" { reason = "startup_failed" }` block.
Immediately after constructing `s.outcome`, add the refresh:

```go
s.outcome = &eventlog.Outcome{Reason: reason}
s.refreshExecutionOutcomeLocked()
```

The remaining existing Open -> Closing transition is unchanged: seal admission,
call workflow.BeginClosing, stop telemetry, cancel execution, then asynchronously
finalize after admitted operations and agent loops settle.

In `finalize`, after setting `s.outcome.CleanupError`, replace the old
`executionError` and `startupError` string-assignment blocks with:

```go
s.refreshExecutionOutcomeLocked()
```

`workflow.Close` has joined the dispatcher by this point, so this refresh also
captures the cause when user Close wins the race against the supervisor. It runs
on every resource-cleanup retry, preserving the original execution failure while
allowing CleanupError to clear. `cleanupOK` still depends only on actual cleanup;
the session reaches Closed after resources settle even though Outcome.Error is
nonempty. The accepted log is sealed with that outcome, remains readable, and is
released only by an explicit Dispose.

Do not call `s.log.Fail(dispatchErr)`: a failed reader is not evidence that the
already accepted log is unwritable or corrupt. Do not synchronously call Close
inside the workflow reader/dispatcher: that would join the goroutine executing
the callback.

## Focused regression tests

1. Inject a one-shot event-store read failure after startup through the existing
   `Dependencies.EventStore` seam. Start a provider that waits on cancellation.
   Assert admission rejects Send/CreateAgent/AssignWork, provider cancellation is
   requested, Inspect shows Closing + workflow_failed + the exact cause, and
   previously accepted records remain readable after the one-shot failure clears.
2. Hold the provider's unwind after cancellation: outcome must already expose
   the dispatcher failure while Close waits. Release it; assert Closed, resources
   closed once, no orphaned dispatcher/reader goroutine, log sealed and readable.
3. Return a malformed accepted ack/agent_exited record from the injected reader;
   require the decode error to take the same path, with no new assignment receipt
   or work mutation accepted after the failure fence.
4. Normal close and owner cancellation: terminal dispatcher Err is nil, reason
   remains requested/owner_cancelled, tail agent-exit events are retained, and no
   workflow_failed error is invented. Unexpected ErrClosed while still open is
   a failure, while EOF after Controller.Done is orderly termination.
5. Race user Close with a dispatcher read error; run repeatedly under -race. The
   terminal cause must be recorded regardless of which closing trigger wins.
6. Fail the first owned-resource cleanup after dispatcher failure. Assert Closing
   exposes both Outcome.Error and CleanupError. Retry Close: Closed, same original
   Outcome.Error, cleared CleanupError, already-closed resources not repeated.
7. Fail the workflow reader during construction (before root bootstrap / before
   configuration publication / just before supervision). New must fail with
   retained cause or return a session fenced and automatically closing, never
   panic, leak ownership, finalize half-built resources, or silently remain open.
8. Concurrent Interrupt then dispatcher failure: SettleInterrupt preserves the
   dispatcher cause, the interruption attempt finishes, and finalization does not
   deadlock waiting for an interruption that is waiting on a dead dispatcher.

The stored original error plus Done channel is the component's single terminal
result; the harness Outcome is its existing user-facing projection. No automatic
restart is proposed because replaying delivery after an uncertain failure needs
an independent delivery/idempotency contract.
