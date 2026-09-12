# ADR-001: A session owns the harness independently of adapters

## Context

Strap already separates agent execution, message routing, the work ledger, model
providers, and tools. The CLI still selects prompts, providers, role capabilities,
and resource lifetimes. Some workflow operations are available only through model
tool callbacks. A future HTTP adapter or eval runner would otherwise have to
reproduce that assembly and could execute different behavior.

## Decision

Introduce a public `harness.Session` that owns one root, its delegated agents,
work ledger, resolved configuration, execution lifetime, and observation history.
The TUI, HTTP adapter, and direct Go callers use the same application operations.
Existing domain packages retain their responsibilities; workflow coordination may
remain internal. The session does not expose mutable controller or store fields.

Model tools and trusted host commands call shared workflow operations. Actor
identity remains runtime-bound for model tools; host authority is explicit and
cannot be inferred from an actor ID supplied over a transport.

Observers have independent cursors into retained session events. Observation does
not drive dispatch. Detaching an observer never closes a session. The owner may
explicitly choose to close a session when its application exits. Shutdown seals
command admission, joins execution, drains final events, and finalizes owned
execution resources before sealing observation history. An owner explicitly
disposes storage after readers no longer need it. Active telemetry and other
resource I/O participate in shutdown alongside agent execution.

Use a small event-store interface with a bounded in-memory implementation first.
Subscriptions share one cursor contract across future storage backends. Publisher
overload or storage failure is explicit and makes capture incomplete. Lifecycle
state revisions remain separate from conversation-history revisions. Agent-bound
tool handles may share resources owned by their session.

Managed resources expose `Close(context.Context) error` with explicit ownership.
Each session owns a dedicated provider HTTP transport and its connection pool;
provider clients borrow it, including for token counting. Shutdown joins active
requests before releasing connections. Borrowed custom dependencies retain their
external ownership. Diagnostics share the session's canonical event publication.

Use one session per eval trial initially. Trial selection, grading, and aggregate
scores remain in the eval runner. After the memory store, implement opt-in JSONL
recording for persistent tool diagnostics using the same storage contract. Coverage,
capture health, and retention are distinct. HTTP routing, SQLite, complete eval
archives, execution recovery, and a separate run abstraction are subsequent work.

## Consequences

- Shared assembly and command implementations make adapter parity testable with
  scripted providers, without promising deterministic model output or scheduling.
- This is an extraction with lifecycle and observation changes, not a rewrite of
  the conversation controller or work ledger.
- A public session is an additional API commitment. Introduce working vertical
  slices rather than speculative registries, empty interfaces, or unenforced limits.
- Independent readers need explicit retention, gap, payload ownership, and closing
  contracts. A bounded event log alone does not bound the entire runtime's memory.
- Internal workflow callbacks and CLI assembly must be refactored before HTTP can
  provide the full harness. Merely wrapping `conversation.Controller` is insufficient.

Implementation is pending. See [the audited design](../../HARNESS_DESIGN.md).
