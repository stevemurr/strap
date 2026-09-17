# ADR-003: Stop current work without terminating the conversation

## Context

Strap sessions and agents persist across user messages. Pause waits for an
operation boundary and resumes the same execution; `StopAgent` permanently ends
an agent. An editor Stop button needs to cancel work promptly and keep the
conversation available for another instruction, including when root is waiting
for delegated work. Cancellation can arrive after an external effect or after an
assistant tool batch has entered history.

## Decision

Expose the host operation `harness.Session.Interrupt(ctx)`. `/stop` and HTTP
`POST /sessions/{id}/interrupt` call it. Keep `StopAgent`, the model's `stop_agent`,
and the existing per-agent HTTP `/stop` route terminal; the TUI names that action
`/terminate [id]`. Quit and `Close` retain shutdown semantics.

Interrupt fences new mutations, agent creation, and routing; cancels each live
agent's current exchange; then joins execution and already admitted mutations.
It preserves completed tool results, records partial results, and adds explicit
unexecuted results for the remaining calls in a committed batch. Partial model
text remains observable but is not committed as a successful assistant message.
Every live agent enters the nonterminal `interrupted` state.

After agents settle, queued deliveries receive undelivered receipts. All
nonterminal delegated work is canceled using current ledger revisions; completed
work, submissions, research artifacts, plans, and file effects remain. The
dispatcher retires pending assignment/progress notices without waking agents.
A subsequent accepted user `Send` releases the hold. Pause/resume cannot release
an interruption. The next instruction sees retained history and fresh work state.

The caller's context bounds its wait only. Repeated interrupt calls join one
attempt until another user message is accepted. A timeout preserves ownership and
the execution fence. Close joins interruption before disposing owned resources.
Capture/settlement failure never reopens admission or reports successful Stop.
Schema 5 adds `interrupted`; readers retain support for schemas 2–4.

## Consequences

- TUI, HTTP, and future ACP callers share cancellation and continuation behavior.
- Stop is session-wide: concurrent work belongs to the persistent session, and
  current execution does not have isolated per-prompt ownership.
- Interruption does not undo effects, retry tools, or restore execution after a
  process restart. Dependencies that ignore cancellation delay settlement.
- Queued input is explicitly discarded; submit the next instruction after Stop
  finishes. Canceled work must be newly assigned if the next task needs it.
- Tests cover cancellation during streaming and tools, delegation, queued input,
  pauses, timeout ownership, continuation, adapters, and shutdown races.
