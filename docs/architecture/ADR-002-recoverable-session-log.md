# ADR-002: Recoverable session views from one ordered log

Status: accepted design direction; implementation pending.

## Context

The session foundation decouples execution from its TUI, HTTP, and SDK observers.
Its bounded observation log can evict events or omit oversized payloads, and
independent runtime inspections do not form a consistent reconstruction boundary.
Streaming makes these limitations visible: a newly attached view must recover
accepted history and generation in progress, even after a long disconnection.

## Decision

Use one recoverable session log, one replay-then-follow subscription contract,
and view projections derived from accepted records. A fresh client starts at the
beginning; a client retaining its projection resumes after its last applied cursor.
No public snapshot/bootstrap protocol is required. Checkpoints may later optimize
replay while preserving this contract.

Required session content remains readable until explicit disposal. Storage owns
append/read ordering and race-free notification; the harness owns authorization,
publication, and lifetime. Full required content is retained inline or as bounded
content-chunk records in the same log. Memory storage grows with retained history;
disk storage can bound working memory. Quota/write failure is explicit and stops
affected execution rather than silently compromising reconstruction. Optional
diagnostic exporters are not authoritative storage.

Providers emit text through one submission contract. Agent-owned output lifecycle
records correlate deltas, validated history commitment, usage, and routed messages.
Failed partial text remains inspectable without entering model history. Views only
detach; hosts explicitly close/dispose sessions. Replay has no execution effects.

## Consequences

- This supersedes ADR-001's lossy observer-retention policy for required session
  records and its policy of continuing execution after required capture failure.
  ADR-001's domain ownership, resource lifetime, and client independence remain.
- Current store implementations, callbacks under locks, record coverage, TUI input
  rendering, and public provider/subscription contracts require migration.
- A log cursor describes a publication prefix, not an atomic runtime checkpoint.
  Inspection read models expose their applied cursor and remain derived data.
- Slow clients cannot stall execution. Slow required storage can apply backpressure;
  storage failure cannot roll back external effects and must never trigger blind retry.
- Client reconnection does not imply crash durability, resumable agent execution,
  exact model-request archives, or deterministic replay of external effects.

See [the revised design and audit](../../STREAMING_DESIGN.md) for contracts,
failure behavior, migration requirements, and acceptance tests.
Concrete proposed package boundaries and Go shapes are recorded in
[Types and interfaces](STREAMING_CONTRACTS.md).
