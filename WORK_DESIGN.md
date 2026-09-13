# Work, plans, and audits

Status: implemented. This describes the contract in `work`, with application
wiring in `internal/workflow` and tools in `tool/work.go`. See [DESIGN.md](DESIGN.md)
for the surrounding conversation core.

## Model and ownership

One conversation-scoped, in-memory `work.Store` is the authoritative record of
plans, delegated work, submissions, and audit outcomes. It is not an append-only
history of every edit. A plan contains shared steps; work assigns responsibility
for a subset of those steps. Passing every current step delegates the whole plan.
Later additions do not expand an existing scope.

Implementation, audit, and repair are kinds of `Work`. `message.Assignment` is
removed, and messages carry independent `work.Work` snapshots. A copied or
forwarded snapshot never creates authority. Store operations validate the runtime
caller against the current record.

```text
Root owns plan
  → create_agent(role=implementor): idle registered agent
  → assign_work: implementation to explicit assignee over selected step IDs
  → implementor updates shared progress
  → submit_work: immutable outcome, needs_check
  → root receives review request
  → create_agent(role=auditor), or select an existing auditor
  → assign_work: audit to explicit assignee
  → submit_audit
      pass → implementation accepted, scoped steps completed
      fail → immutable findings, original changes_requested
               → root assign_work(kind=repair, assignee, original work/revision, audit_id)
               → submit_work: superseding outcome, needs_check
```

The root explicitly chooses an existing implementor for each repair; it may reuse
the previous implementor or create a fresh one. The store derives scope and context
from the immutable audit and its exact source submission. Repair reassignment does
not change the original work's assignee or grant the replacement broader reads.
The auditor records findings but does not create or assign repair work.

## Package boundaries

| Package | Responsibility |
|---|---|
| `roster` | Application role, creation request, registration, and eligible work kinds |
| `identity` | Dependency-free `ActorID`; shared by work and messages |
| `work` | Records, scope reservations, authorization, transitions, snapshots, pending events |
| `message` | Envelopes containing `*work.Work`; transport identity and receipts |
| `tool` | Operation-specific schemas and application callbacks |
| Application wiring | Select agent specs, register work, dispatch events, handle delivery failures |
| `conversation` / `agent` | Routing, lifecycle, inbox consumption, model/tool loop |
| `internal/tui` | Display shared progress and work events |

`work` imports neither `message`, `agent`, `conversation`, nor `tool`. Initially,
`message.ActorID` can alias `identity.ActorID` to reduce migration churn. The
application's auditor actors do not perform implementation or repair work; this
prevents self-review after reassignment without a general role system in `work`.

## Core types

IDs are distinct string types: `ID`, `PlanID`, `StepID`, `SubmissionID`, `AuditID`,
and `EventID`. `Revision` is a `uint64` type. `ActorID` comes from `identity`.

```go
type Plan struct {
    ID       PlanID
    Owner    identity.ActorID
    Revision Revision // Structural edits; not delegated progress.
    Title    string
    Steps    []Step
}

type Step struct {
    ID                 StepID
    Title              string
    AcceptanceCriteria []string
    Status             StepStatus
    Note               string
}

type Scope struct {
    PlanID  PlanID
    StepIDs []StepID
}

type Work struct {
    ID       ID
    Kind     Kind
    State    State
    Revision Revision

    Owner              identity.ActorID
    RequestedBy        identity.ActorID
    Assignee           identity.ActorID
    AssignedAtRevision Revision

    Task           string
    Context        string
    ExpectedOutput string
    Scope          *Scope
    Note           string
    Blocker        string

    ParentID            ID
    SubjectSubmissionID SubmissionID
    RequestedByAuditID  AuditID
    LatestAuditID       AuditID
    ActiveRepairID      ID
    LatestSubmissionID  SubmissionID
}

type WorkTarget struct {
    ID               ID
    ExpectedRevision Revision
}
```

`WorkTarget` replaces the earlier name `WorkVersion`: it identifies an operation's
target and rejects stale updates; it does not track repairs. `AssignedAtRevision`
captures the work revision when the recipient is bound, remaining stable during
progress updates. Receipt handling uses it to distinguish old assignments from
replacements.

Audit and repair work have the implementation's ID as `ParentID`. An audit targets
one immutable submission; a repair points to the audit outcome that requested it.
The store derives these relationships. `Scope` may be absent for a standalone
task; its submission is reviewed against the captured task and expected output.

Step statuses are `pending`, `in_progress`, `blocked`, `ready_for_review`,
`completed`, and `cancelled`. Readiness is the implementor's claim; completion
requires a passing audit. There is no per-step revision or separate grant record. Submission results and
reads are filtered for narrow repair actors; owner/auditor snapshots retain the
complete original scope.

## Submissions, verdicts, and blockers

| Type | Contents |
|---|---|
| `Submission` | ID, original work ID, submitting implementation/repair ID, submitter, superseded submission ID, captured task/expected output/steps, summary, evidence, artifact references |
| `ArtifactRef` | URI/path and optional version identifier, such as a commit or digest |
| `Finding` | Scoped step IDs, deficiency, required change, verification criteria |
| `Audit` | ID, auditor work ID, exact submission ID, reviewer, verdict, summary, findings; legacy repair work ID is read-only historical data |

Submissions and audit outcomes are immutable. An artifact reference can identify
a generated or modified document, code, image, or report. It does not prove that
the reference is immutable or that an auditor inspected those bytes. Artifact
resolution and verification belong to application tooling.

Verdicts are `pass` and `fail`. Failure requires findings and atomically records `LatestAuditID` and requests changes.
The root must explicitly assign repair work for the affected subset. Acceptance covers the complete submitted
scope; partial acceptance is deferred. Repair submission creates a new outcome
for the original scope, ready for another audit.

Submission requires the authorized scoped steps to be ready and the submitting
work's blocker to be clear. A failed audit reopens the steps named in its findings;
the unaffected steps remain ready for review. A passing verdict is rejected while
the auditor still reports a blocker.

Being unable to verify is not a failing verdict. The auditor updates
`Work.Blocker`; audit work remains `active`, implementation work remains
`checking`, and the owner receives an actionable notification. Clearing the
blocker allows review to continue. No repair or verdict is created. There is no
`blocked` or `inconclusive` verdict in this version.

| Work kind | Lifecycle |
|---|---|
| Implementation | `active → needs_check → checking → accepted`; rejection enters `changes_requested`, repair submission returns to `needs_check` |
| Audit | `active → closed` when a verdict is committed |
| Repair | `active → closed` when repairs are submitted |
| All | Explicit cancellation is possible |

Agent idle, stopped, and failed states are separate from work state. A text reply
does not submit or accept work. Delivery failure does not itself reject or cancel
the work; the owner must be informed and able to recover it.

## Operations and inputs

Use one concrete synchronized `Store`; define consumer-local interfaces only when
a real substitution requirement appears. These are synchronous in-memory
operations. Tool adapters retain cancellation contexts and supply trusted actor
identity separately from decoded arguments.

```go
func New() *Store

func (*Store) UpdatePlan(identity.ActorID, PlanUpdate) (Plan, error)
func (*Store) AssignWork(identity.ActorID, AssignRequest) (Work, error)
func (*Store) UpdateProgress(identity.ActorID, ProgressUpdate) (Work, error)
func (*Store) SubmitWork(identity.ActorID, SubmitRequest) (Submission, error)
func (*Store) AssignAudit(identity.ActorID, AssignAuditRequest) (Work, error)
func (*Store) SubmitAudit(identity.ActorID, AuditRequest) (Audit, error)
func (*Store) AssignRepair(identity.ActorID, AssignRepairRequest) (Work, error)
func (*Store) Reassign(identity.ActorID, ReassignRequest) (Work, error)
func (*Store) Cancel(identity.ActorID, CancelRequest) (Work, error)

func (*Store) GetPlan(identity.ActorID, PlanID) (Plan, error)
func (*Store) GetWork(identity.ActorID, ID) (Work, error)
func (*Store) GetSubmission(identity.ActorID, SubmissionID) (Submission, error)
func (*Store) GetAudit(identity.ActorID, AuditID) (Audit, error)
```

`AssignWork`, `AssignAudit`, and `AssignRepair` assign responsibility; neither claims that
execution has started. They replace the earlier `Assign` / `StartAudit` wording.

| Input | Fields and restrictions |
|---|---|
| `PlanUpdate` | Optional plan ID and expected structural revision; optional title; step additions/edits, ordering, cancellation |
| `AssignRequest` | Application-validated assignee; optional scope; task, context, expected output. Creates implementation work only |
| `ProgressUpdate` | `WorkTarget`; optional work note/blocker; scoped step status/note patches |
| `SubmitRequest` | `WorkTarget`; summary, evidence, artifact references |
| `AssignAuditRequest` | Implementation `WorkTarget`, current submission ID, application-validated auditor |
| `AuditRequest` | Audit `WorkTarget`, submission ID, pass/fail verdict, summary, findings |
| `AssignRepairRequest` | Original implementation `WorkTarget`, current failing audit ID, required existing implementor assignee |
| `ReassignRequest` | `WorkTarget`, required existing replacement assignee |
| `CancelRequest` | `WorkTarget`, reason |

`UpdatePlan` creates when the ID is omitted and patches when it is supplied. An
unknown supplied ID is not-found, never an implicit creation. Explicit empty/null
IDs are invalid tool input. Creation requires a title and initial steps; it
rejects existing IDs and expected revisions. Updates require the current
structural revision. Omitted fields remain unchanged. No extra action selector,
current-plan lookup, separate create API, or arbitrary status setter is needed.

Structural editing cannot change requirements or cancel steps reserved by work.
The owner must explicitly resolve the affected work first. Completed work cannot
be rewritten in place to retain acceptance for changed requirements. New work
must represent a changed outcome. Unrelated delegate progress does not invalidate
the owner's structural revision.

The `update_plan` tool composes separate typed operations: creation without IDs,
structural editing with `plan_id`, and progress with `work_id`. Creation requires a
title and new steps; progress-only fields cannot appear in structural operations.
The root receives creation/editing, and implementors receive progress. Auditors
use `update_work` for notes/blockers and cannot advertise implementation step edits.
Conflicting selectors cannot match any operation.

`assign_work` composes implementation, audit, and repair argument contracts; `submit_audit`
composes pass and fail contracts. Pass accepts omitted or empty findings; fail
requires at least one finding.
`submit_work` maps to its store operation. `get_audit` resolves the immutable
outcome identified by an event’s audit ID. The tool package owns JSON contracts;
the work package owns state and authority.

## Enforced boundaries

- Runtime actor, current work revision/state/kind, and permitted scope are checked
  under the same lock as mutation. Invalid batches make no partial changes.
- The implementation reserves its subset through review. During repair, only
  the linked active repair can update its selected steps. Unrelated work cannot
  overlap that reservation.
- Delegates update status/notes, not requirements. Auditors update their own
  note/blocker and submit verdicts, not implementation progress.
- Owner plan edits cannot mark delegated work completed to bypass an audit.
- A verdict must target the current submission and active audit; stale or repeated
  verdicts cannot accept new work or issue another repair.
- The auditor cannot select a different repair recipient or expand its scope.
- Reassignment changes revision and binding. Replacing an implementor during
  review requires explicitly resolving the affected review cycle first.
- Reads, errors containing current state, and routed events respect scope.
  Snapshots deeply copy pointers and slices across store/message/provider/host
  boundaries. Mutating a returned `Work` never mutates the store.
- Shell/file execution is not fenced by work revisions. Workspace isolation and
  stopping already-running side effects are outside this package's guarantees.

## Events and delivery

Committed work transitions enqueue operational events: `WorkAssigned`,
`ProgressChanged`, `ReviewRequested`, `AuditCompleted`, `WorkReassigned`, and
`WorkCancelled`, plus host-only `PlanChanged` for structure edits. Each event has an ID, actor, independent work/step snapshots,
and relevant submission/audit IDs. Blocker changes make progress events actionable
for the owner. Events may also be displayed or logged for debugging.

```go
func (*Store) PendingEvents(limit int) []Event
func (*Store) AcknowledgeEvent(EventID) error
```

Application wiring handles dispatch outside the store lock. Pending events remain
until their effects are handled. Owner notification events are acknowledged
only on consumed receipts; undelivered notifications remain pending. Assignment
events are acknowledged when queued and tracked with existing receipts. Cancellation/reassignment also informs displaced actors at their next boundary;
already-running operations are not interrupted. Do not retry ambiguous deliveries
automatically. There is no durable or exactly-once delivery promise.

Record message ID, work ID, recipient, and assignment binding for lifecycle and
delivery handling. A late failure for an old binding cannot invalidate a new one.
Stale pending assignment events must be suppressed after cancellation/reassignment.

Routine progress updates refresh shared views and can enter the root's context
at its next active boundary without waking it. Review requests and blocker changes
wake the owner. Work/submission correlation stays separate from the user-message
reply target. Merely adding another message kind does not implement this behavior.

`conversation.NextEvent` currently has one consumer. The application must relay
events to both workflow handling and the UI, rather than adding competing readers.
Dispatch must operate independently of whether a UI consumer is currently reading.

## Implementation layout

1. **Identity and store foundation.** `identity` and `work/store.go` provide actor
   IDs, plan edits, scope reservations, progress, revisions, reads, and events.
2. **Review cycle.** `work/review.go` implements immutable submissions/audits,
   atomic verdict effects, linked repairs, cancellation, and reassignment.
3. **Transport.** `message` carries deeply cloned work/event snapshots;
   `agent/agent.go` separates notification wakeup from reply correlation.
4. **Application and tools.** `internal/workflow` and `tool/work.go` register work,
   provision role-specific agents, dispatch events, and handle delivery failures.
5. **Host and examples.** The CLI relays workflow events to the TUI. The scripted
   delegation example exercises fail → repair → pass without a model server.

The application exposes one registered creation path, `create_agent(role)`, separate
from `assign_work`. The root is registered during bootstrap. Raw controller creation
remains idle and does not confer tracked-work eligibility. The old task-only
creation callback has been removed; core tests define a local `create_test_agent`.

`LatestAuditID` changes only on verdict. `ActiveRepairID` changes on explicit repair
assignment, submission, or cancellation. One repair may exist per audit; duplicate
or concurrent requests fail without creating additional work. Repair submission
requires that exact active repair and clears the pointer. `get_work(repair)` returns
the original context, exact source submission's evidence/artifacts, and immutable
audit, with step snapshots filtered to repair scope even for the owner. Current
noncancelled repair assignees can read that source submission and audit; reassignment
or cancellation revokes derived rights. Independent owner/submitter/auditor rights
remain. Evidence and artifact references are shared at submission level.

Implementation cancellation cascades to live audit/repair work, resets reserved
steps to pending, and releases reservations. Cancelling repair ends that original
cycle too. Cancelling an audit alone returns its unchanged submission to
`needs_check`. Recovery after actor exit only applies to active execution; a
submitted outcome remains reviewable after the implementor exits.

## Verification checkpoints

- Creation versus patching, unknown/null IDs, atomic invalid patches, and owner
  restrictions; no implicit acceptance through plan edits.
- Disjoint work can progress independently; overlapping reservations fail;
  stale calls after reassignment or cancellation cannot write.
- Submission suspends writes; stale audit verdicts fail; failure creates no repair; explicit assignment creates exactly
  one repair per audit for the derived scope; repair submission requests a new
  audit; passing completes the whole original subset.
- Auditor blockers wake the owner without verdicts or repairs. Clearing a blocker
  permits continuation. Configured auditor actors never implement or repair.
- Deep snapshot isolation, scoped reads, and provider serialization of Work.
- Immediate and late delivery failures, old-binding events, unavailable recipients,
  and cancellation while events are pending remain observable and recoverable.
- Ordinary progress does not trigger model calls by itself; review/blocker events
  do; neither replaces user reply correlation. Workflow works without a UI reader.
- Run `go test -race ./...`, including a scripted end-to-end audited delegation.

Deferred: persistent storage, a general outbox/replay or idempotency framework,
transparent creation retries, arbitrary nested delegation, partial acceptance,
configurable workflow policies, automatic artifact management, and workspace
isolation. State/revision checks prevent repeated transition effects; creation
does not promise retry deduplication.
