# Shared work progress, research, and root commentary

Status: revised design, 2026-09-13. Incorporates the taxonomy audit and subsequent
plan/todo clarification, the seven contract audit findings, bounded researcher
execution, and the follow-up audit of wakeups, yielding, and cursor access.
Implementation checkpoints are recorded in [RESEARCH_IMPLEMENTATION.md](RESEARCH_IMPLEMENTATION.md).
Runtime, role prompts, progress presentation and optional response disclosure
are implemented. Deterministic end-to-end acceptance passes; the bounded live
comparison is pending a reachable model server. See [validation results](RESEARCH_VALIDATION.md).

### Implementation status

The implementation includes the researcher role and Delivered lifecycle, immutable
briefs and progress, explicit worker API migration, bounded authorized readers,
selective notices, coverage and new-exchange admission, inbox yielding,
assignment-bound diagnostics, host-issued evidence, and partial cancellation capture.
The selected names are `submit_work` and `wait_for_input`. The proposed generic
execution-work kind and automatic dependency scheduler remain outside v1.
Detailed tests and commit boundaries are in the checkpoint tracker; static
validation alone does not establish live-model behavior.

The researcher/planner investigates a bounded question and reports evidence,
uncertainty, and next steps. All worker roles use the same work-progress vocabulary.
The harness retains reports and applies authorized step updates. The root owns the
user conversation and plan structure, and explains developments that matter.

The existing shared plan remains the todo list. Add evidence-bearing progress
reports to the existing work ledger; do not create separate plan, todo, and progress
databases that each claim to own step status. Root narration is an interpretation
of recorded developments, not the authority that keeps the task list synchronized.

## 1. Boundaries and existing foundations

| Layer | Responsibility |
| --- | --- |
| `roster` | Researcher role and eligibility for research work |
| `work` | Assignment authority, reports, findings, research briefs, revisions |
| `internal/workflow` | Role tools, assignment delivery, notification policy |
| `agent` | Sequential model/tool execution, publishing commentary, yielding |
| `harness` | Assembly, public operations, authoritative event recording |
| `harness/inspection` | Authorized reads of the same recorded state, live or archived |
| `internal/tui` | Root commentary, worker detail, disclosure and display state |

Extend the existing work ledger and passive read model. Do not create a second
mutable status store maintained by the root. Runtime state remains authoritative
in agent lifecycle records; researcher reports cannot set it.

### Shared objects and authority

| Object | Meaning | Authority |
| --- | --- | --- |
| Plan | Shared todo list: step definitions, acceptance criteria, ordering, statuses | Root edits structure; harness applies authorized progress and audit transitions |
| Work item | Assignment, owner, assignee, lifecycle, optional subset of plan steps | Existing work commands and role checks |
| Progress report | Attributed position, evidence, discoveries, uncertainties, optional step-status changes | Current assignee reports; harness validates and records |
| Delivered outcome | Implementation submission, research brief, or audit verdict | Role-specific submission contract |
| Agent state | Running, idle, paused, failed, stopped | Runtime lifecycle events |

A plan step has one authoritative current status. A report can record the change
and its explanation, but the progress read view obtains current step status from
the plan. It does not maintain a second independently editable step table.

Before this revision an implementor called `update_plan` with a work ID and revision. The operation
already writes statuses/notes directly to its scoped plan steps through
[Store.UpdateProgress](../../work/store.go). The root does not copy that report
into the plan. Auditors previously reported notes/blockers through `update_work`, reaching the
same domain operation with narrower capabilities. A passing
[audit](../../work/review.go) marks implementation accepted and scoped steps completed.

The new `report_work_progress` consolidates those worker paths without changing
that authority. Keep `update_plan` for root-owned structure. A worker may report
pending/in_progress/blocked/ready_for_review on eligible assigned steps, never
completed. A research finding or proposed step does not automatically edit the plan;
the root decides whether to change structure or assign further work.

Current foundations:

- [Role registration](../../roster/types.go) supports implementor, auditor and researcher.
- [Work commands](../../work/commands.go) separate creation, assignment, inspection,
  and revision-checked mutations.
- [Work reporting](../../work/reporting.go) publishes complete accepted changes
  before exposing events to the dispatcher; publication failure poisons mutations.
- [Passive reconstruction](../../work/readmodel.go) applies changes without dispatch.
- [Commentary routing](../../conversation/controller.go) uses `agent.Reporter`;
  the legacy `OnCommentary(string)` callback is not the controller's active path.
- [Agent execution](../../agent/agent.go) consumes observations without waking an
  idle model, and treats a text-only response as a reply ending the exchange.

All Go declarations below are proposed API shapes, with repetitive JSON tags
omitted. Wire fields use snake_case and reject unknown fields. These are additions
or sketches of extensions, not replacement definitions of existing structs.

## 2. Research role and lifecycle

### Role inputs, outputs, and required sequencing

The existing registered roles are root, implementor, and auditor; researcher/planner
is the proposed addition. Implementation and repair are distinct work kinds handled
by the implementor role. Repair is not a separate agent role or a generic label for
any code fix. Preserve these input/output expectations when adding research:

| Role / work kind | Required input | Required output and next state |
| --- | --- | --- |
| Implementor / implementation | Assignment packet: task, context, expected outcome, optional plan scope | Completed implementation submitted with summary/evidence/artifacts, ready for independent audit; original work becomes NeedsCheck |
| Auditor / audit | A specific latest implementation submission ready for audit, its requirements and evidence | Pass accepts the implementation and completes its scoped steps; fail records actionable findings and moves it to ChangesRequested |
| Implementor / repair | A failed audit for the current submission, including scoped defects, required changes and verification expectations | Corrections submitted as a superseding submission of the original implementation; repair item closes and original returns to NeedsCheck |
| Researcher / research | Bounded question or planning assignment | Evidence-backed brief, uncertainty, and proposed next steps; delivery grants no implementation acceptance |
| Root / coordination | User request and the above reports/outcomes | Explicit assignments, plan decisions, and user-facing synthesis |

The default root prompt selects the agent workflow for explicit research,
investigation, source review, implementation planning or change requests, and for
answers requiring source inspection, commands, tests or file changes. Root creates
a shared plan, breaks the task into phases, and uses create_agent/select plus
assign_work to delegate each phase. Investigation goes to researchers;
implementation goes to implementors and requires independent audit acceptance.
One assignment can be one phase. Direct responses cover greetings, clarification
and questions answerable from the conversation or general knowledge without those
operations. Status replies read existing records and retain the current workflow.

Root reads conversation and recorded plans, work, progress, findings, briefs and
audits to coordinate and explain results. Additional source inspection belongs to
a researcher, including inspection needed to prepare later assignments. Planning
and delegation are behavioral instructions; the underlying APIs still permit
unscoped implementation and expose local tools to root.

Mandatory planning also applies to research-only requests. The current lifecycle
cannot mark research plan steps completed: research assignments are unscoped,
root edits only plan structure, and implementation audit acceptance alone completes
steps. Delivered research plus root synthesis can fulfill a research request,
while those plan steps remain pending. The prompt must describe that result
accurately rather than invent an implementation audit or a status mutation.
Supporting completed research steps requires a separate lifecycle design change.

The implementation cycle is:

```text
Implementation assignment
  → completed submission ready for audit
  → root assigns audit of that submission
      → pass: implementation accepted; scoped steps completed
      → fail: findings recorded; changes requested
          → root assigns repair based on that failed audit
          → corrected implementation submitted
          → root assigns audit of the new submission
          → repeat repair/audit until pass, or explicit cancellation/blocker handling
```

The harness enforces transitions and reports events; the root explicitly assigns
each audit and repair. A failure does not automatically create an agent or dispatch
a repair. Repair task/context/scope are derived from the failed audit and original
work; the root does not replace them with an unrelated new task. An auditor must
be independent of implementation/repair contributors and does not fix its own
findings. A previous eligible auditor may review the corrected submission.

"Ready for audit" means the implementor has finished the assigned outcome, cleared
its blocker, and made scoped steps ready_for_review. It does not mean the outcome
is already accepted. If an auditor cannot verify, it reports a blocker; that is
different from a failed verdict with actionable defects. Final prose alone never
substitutes for submission or a verdict.

A researcher discovering a bug before any failed audit reports the finding. If the
root decides to change code, that starts implementation work, not repair work.
Likewise an ad hoc diagnostic check or speculative plan review is research, not
an audit assignment in this implementation lifecycle. Bounded researcher execution
does not broaden auditor inputs or bypass implementation submission.

These rules are grounded in [role eligibility](../../roster/types.go),
[submission/audit/repair transitions](../../work/review.go), and
[root coordination instructions](../../harness/prompts.go).

### Research addition

Add `roster.Researcher = "researcher"`, `work.Research = "research"`, and
`work.Delivered = "delivered"`. Researcher accepts only research work in v1.
Add `Researcher AgentConfig` and `Researcher AgentDependencies` beside existing
role configuration, including independent provider overrides and effective config.

Use the existing create/assign workflow:

```json
{"role":"researcher"}
```

```json
{
  "kind":"research",
  "assignee":"agent-2",
  "task":"Compare the repository against PLAN.md",
  "context":"Analysis only; distinguish verified gaps from untested concerns.",
  "expected_output":"An evidence-backed gap analysis and proposed next steps."
}
```

Research assignment requires an eligible existing assignee and a task. For v1 it
rejects implementation `scope`, submission, audit, and repair selectors. It does
not reserve implementation plan steps. The root can reference a plan in context.

Transitions:

| Operation | Before | After |
| --- | --- | --- |
| Assign research | No item | Active |
| Report progress | Active | Active, revision incremented |
| Reassign | Active | Active, new assignment binding |
| Submit research | Active | Delivered, immutable brief attached |
| Cancel | Active | Cancelled |

Delivered means a research brief was supplied, not that every claim was verified
or an implementation passed audit. Delivery is terminal for this work item.
Follow-up investigation gets a new research assignment. A final text reply cannot
deliver research. Research cannot call `submit_work` or `submit_audit`.

The researcher proposes plan steps; only the root calls structural `update_plan`.
Delivery never accepts implementation work or marks its scoped steps completed.

V1 is planned to expose file/PDF reads, available search tools, bounded diagnostic
shell execution, reporting, work inspection, research submission, and messaging.
The researcher runs ordinary build/test/inspection checks itself. Root-owned
implementors handle implementation and failed-audit repair assignments;
there is no executor-agent hop required for every command. Section 10 defines the
execution bounds and their limitations. This is a planning choice, not a change
to current runtime capabilities.

### Research lifecycle migration (audit concern 3)

Adding an enum is insufficient. Replace implicit kind fallthrough with exhaustive
kind handling in Store.Cancel, assignment/reassignment, submission, dispatcher,
listing, projection validation, and presentation:

- Research cancellation changes only that research item to Cancelled, records the
  reason, and notifies its owner. It creates no parent work, submission, audit,
  plan-step change, or review_requested event.
- Add Delivered to the shared terminal predicate and all terminal/active filters.
  Reject cancel, reassign, report, and repeated submission after delivery; a plain
  final reply still has no work mutation effect.
- Preserve implementation/repair/audit branches. In particular, current
  [Store.Cancel](../../work/review.go) treats the remaining kind as audit and
  modifies its parent; research must never enter that fallback.
- Reject unknown kinds explicitly. Test research cancellation with no parent and
  prove no empty-ID work record or audit event is produced.

## 3. Shared work-progress reports

Use `report_work_progress` and `get_work_progress` across implementors, auditors,
and researchers. This expresses an assignment-scoped report, not agent lifecycle
status or a research-specific inspection umbrella. Reporting does not submit an
outcome, confer acceptance, or change assignment ownership.

Place domain types in `work`; reuse `identity` and `work.Revision`. Existing
`work.Finding` means an audit defect with required change and verification. Do not
redefine it for general discoveries. Use distinct `ProgressFinding` types; a later
internal rename to `AuditFinding` is optional and must preserve audit wire semantics.

```go
type ProgressReportID string
type ProgressFindingID string
type ResearchBriefID string

type EvidenceRef struct {
    URI      string // file/URL/artifact, or host-issued execution reference (§10)
    Revision string // commit/content revision when known
    Locator  string // line range, section, test case, or output location
    Detail   string // what this source supports; not the full payload
}

type FindingBasis string // "observed", "inferred", "retracted"

type ProgressFindingDraft struct {
    Claim      string
    Basis      FindingBasis
    Evidence   []EvidenceRef
    Limitation string
    Supersedes ProgressFindingID // optional correction/retraction
}

type ProgressFinding struct {
    ID       ProgressFindingID
    WorkID   ID
    ReportID ProgressReportID
    Author   identity.ActorID
    ProgressFindingDraft
}

type ProgressDependency struct {
    Need            string // result needed, not a command to the harness
    WorkID          ID     // optional reference to owner-coordinated work
    PreventsFurtherProgress bool // otherwise independent work may continue
}

type WorkPosition struct {
    Objective    string // what this phase is trying to establish; required
    Activity     string // short current action; optional
    Note         string // optional current progress note; preserves existing work.Note
    NextStep     string // intended next action or expected awaited result
    Uncertainty  string
    Blocker      string // intervention required; not ordinary dependency waiting
    DecisionNeed string
    Dependencies []ProgressDependency
}

type ReportWorkProgressRequest struct {
    WorkTarget                  // work_id + expected_revision
    AssignedAtRevision Revision // fences reassignment, even back to same agent
    Position *WorkPosition      // nil preserves position; supplied value replaces it
    Findings []ProgressFindingDraft
    Steps    []StepProgress     // existing type; scoped implementor/repair only
}

type WorkProgressReport struct {
    ID                 ProgressReportID
    WorkID             ID
    Author             identity.ActorID
    AssignedAtRevision Revision
    WorkRevision       Revision // resulting work revision
    RecordedAt         time.Time
    Position           *WorkPosition // position supplied in this report, if any
    Findings           []ProgressFinding
    Steps              []StepProgress // accepted changes, not a second step ledger
}

type ReportWorkProgressResult struct {
    WorkID             ID
    WorkRevision       Revision
    AssignedAtRevision Revision
    ReportID           ProgressReportID
    RecordedAt         time.Time
    FindingIDs         []ProgressFindingID // newly issued IDs; report limits apply
}
```

Contract:

- Only the current assignee of active eligible work may report. Actor identity
  comes from the bound tool runtime or authorized host, never model arguments.
  Enforce equivalent rules for direct Go and HTTP callers.
- Both revision values must match: expected revision checks freshness;
  assigned-at revision fences assignment. No independent mutable progress revision
  is introduced. Step updates preserve the current work-versus-plan revision rules.
- Require at least one position, finding, or step update. Position is optional so
  existing step-only progress remains possible. When supplied, it replaces the
  position; empty optional fields clear previous values. Omitted findings add
  nothing. Omitted steps change nothing.
- Findings accumulate immutably. Corrections reference a current finding in the
  same work; reject unknown targets, already-superseded targets, and cycles.
  Retraction requires a target and explanation. History retains both records.
- Observed claims require evidence; inferred claims also require a limitation.
  Validate shape and locally resolvable references, not factual truth. Reporting
  must not fetch arbitrary URLs. Finding basis remains an attributed claim.
- Steps may change only eligible assigned implementation/repair step statuses and
  notes. Reject structural changes and completed/cancelled status. Auditors and
  unscoped researchers cannot use this field. Findings cannot become audit verdicts.
- Commit the work revision, report, added findings, position, and authorized plan
  step updates in one accepted change. If position is supplied, its note and blocker
  are mirrored into `Work.Note` and `Work.Blocker` atomically for compatibility and
  lifecycle checks. There is no separate independently mutable note/blocker store.
- Deliberately replace legacy progress mutation contracts; no note/blocker patch
  adapter is supported in v1. Reject old requests with an actionable migration
  error before mutation. Never fill in a fresh assignment token for an old caller
  or construct a replacement position from a partial legacy patch (section 9).
- Success waits for required record publication, not for root consumption or
  narration. Recording failure cannot be reported as success.
- Repeating the same expected revision returns conflict and adds no second report.
  This is not exactly-once success: after uncertain delivery, inspect report history
  before retrying with a new revision. Do not blindly manufacture new reports.
- Dependency entries describe reported needs. They do not assign work, start
  execution, pause agents, or automatically establish a scheduling graph. Work IDs
  must be authorized references; they do not grant access to another worker's
  assignment. V1 can omit the ID and have the root carry correlated evidence back.
- Proposed limits: 16 new findings/report, 8 evidence refs/finding, 4 KiB per prose
  field, and 64 KiB encoded/report. Reject overflow explicitly, never truncate evidence.

Example: objective "establish whether executable tests can run"; observed finding
"the authorized build attempt returned parser errors in board.go" with output
reference; uncertainty "runtime behavior remains untested"; next step "continue
source inspection while the root decides whether to assign implementation work".
This does not assert that
all execution paths are impossible or that the entire investigation is blocked.

## 4. Progress inspection and final research brief

```go
type WorkStateSnapshot struct {
    WorkID             ID
    Owner              identity.ActorID
    Assignee           identity.ActorID
    State              State
    WorkRevision       Revision
    AssignedAtRevision Revision
    ActiveBlocker      string // authoritative Work.Blocker; empty once terminal
    CurrentNote        string // compatibility note, cleared on reassignment
}

type ReportedPosition struct {
    Value              WorkPosition
    ReportID           ProgressReportID
    RecordedAt         time.Time // when this position was actually supplied
    AssignedAtRevision Revision
}

type WorkProgress struct {
    Current              WorkStateSnapshot
    LatestReportID       ProgressReportID
    LatestReportedAt     *time.Time // any accepted report, including step-only
    LastReportedPosition *ReportedPosition
    Findings             []ProgressFinding // first bounded page, including inherited evidence
    NextFindingsCursor   string
    Steps                []Step // authoritative plan values at the captured prefix
    MissingStepIDs       []StepID // scoped IDs removed from the plan; never silently omitted
}

type ProposedStep struct {
    Title              string
    AcceptanceCriteria []string
}

type SubmitResearchRequest struct {
    WorkTarget
    AssignedAtRevision Revision
    Summary            string
    FindingIDs         []ProgressFindingID
    OpenQuestions      []string
    Recommendation     string
    ProposedSteps      []ProposedStep
}

type ResearchBrief struct {
    ID                 ResearchBriefID
    Author             identity.ActorID
    RecordedAt         time.Time
    WorkID             ID
    WorkRevision       Revision
    AssignedAtRevision Revision
    Summary            string
    FindingIDs         []ProgressFindingID // immutable finding versions selected at delivery
    OpenQuestions      []string
    Recommendation     string
    ProposedSteps      []ProposedStep
}

type SubmitResearchResult struct {
    WorkID             ID
    WorkRevision       Revision
    AssignedAtRevision Revision
    State              State // Delivered
    BriefID            ResearchBriefID
    FindingCount       int
    RecordedAt         time.Time
}
```

Submission validates the same authority/binding/revision as reporting, requires
a summary, and resolves finding IDs to current findings belonging to this work.
Store immutable finding-version references, not copies of every finding in the
brief response. Following those IDs later returns the selected versions even when
other reports supersede them. Submission returns a compact receipt only.
Bound brief composition to 64 KiB encoded, 256 selected finding IDs, 32 proposed
steps, 32 open questions, and 4 KiB per prose field. Reject overflow before mutation;
the complete report history remains available even when not selected for a brief.
An evidence-free investigation may deliver an explicit inconclusive brief with
open questions; an empty finding list is not proof of success. New evidence must
be reported before submission. Delivery records the brief and transition in one
change, clears any active blocker, and sends an immediate owner notification.
The last position remains historical evidence. Inspection returns authoritative
lifecycle/ActiveBlocker alongside LastReportedPosition at the same log prefix.
Delivery and cancellation clear the active blocker; they do not edit the report.
A step-only report updates LatestReportedAt but never refreshes the position's
own timestamp. A root can distinguish stale reported text from current state
using one progress read (audit concern 4).

Extend `work.Work` with `LatestProgressReportID` and `LatestResearchBriefID`.
`get_work` remains assignment inspection: requirements, ownership, lifecycle,
revision, and outcome references. It may retain existing contextual details for
compatibility, but is not the main progress reader. `get_work_progress` returns
current position, scoped step statuses, and accumulated findings with freshness.
It includes work revision and assignment binding, avoiding an obligatory extra
`get_work` call before reporting. Large collections use pagination; delivery
artifacts have separate immutable reads, not a `get_research` umbrella.

On reassignment, clear last-reported position, current latest-report metadata,
Work.Note, and Work.Blocker. Preserve old
reports and findings with their authorship/binding, exposing them as inherited
evidence. Never present the old assignee's next step as the new assignee's status.

The root and current assignee can read progress detail under existing work
visibility rules. Inspection includes a timestamp, revision, and (at the harness
view boundary) the inspected log cursor. No report means unknown, not no progress.
Runtime state is returned separately by agent inspection. Silence is not failure.


### Scoped reads after completion (audit concern 7)

Authorize get_work_progress through the work item, not through the worker's broad
GetPlan permission. Owner and current assignee may read exactly that work's scoped
step IDs at the captured prefix, including terminal assignments. This grants no
access to other steps or the entire plan. Reassignment revokes the former assignee
under the existing work visibility rule; inherited evidence remains attributed.
Live reads and cursor continuations check current authorization separately from
the captured data prefix, as specified in §5.

The passive read model must assemble these steps with an internal scoped read
rather than silently swallowing a GetPlan authorization error. If the root later
removes a referenced step, return its ID in MissingStepIDs; historical prefix reads
still show the earlier plan. Never substitute a second editable progress-step store.
Test completed and cancelled work, unrelated-step denial, and removed steps.

## 5. Application interfaces and model tools

Public session signatures, delegating through the existing workflow boundary:

```go
ReportWorkProgress(context.Context, identity.ActorID, work.ReportWorkProgressRequest) (work.ReportWorkProgressResult, error)
SubmitResearch(context.Context, identity.ActorID, work.SubmitResearchRequest) (work.SubmitResearchResult, error)
GetWorkProgress(context.Context, identity.ActorID, work.ID) (work.WorkProgress, error)
GetWorkProgressReport(context.Context, identity.ActorID, work.ProgressReportID) (work.WorkProgressReport, error)
GetProgressFinding(context.Context, identity.ActorID, work.ProgressFindingID) (work.ProgressFinding, error)
GetResearchBrief(context.Context, identity.ActorID, work.ResearchBriefID) (work.ResearchBrief, error)
ListWorkProgressReports(context.Context, identity.ActorID, work.ReportQuery) (work.ReportPage, error)
ListWorkProgressFindings(context.Context, identity.ActorID, work.ReportQuery) (work.ProgressFindingPage, error)
```

```go
type ReportQuery struct {
    WorkID ID
    Cursor string
    Limit  int
}
type ReportPage struct {
    Items      []WorkProgressReport
    NextCursor string
}
type ProgressFindingPage struct {
    Items      []ProgressFinding
    NextCursor string
}
```

Use the existing fixed-prefix pagination contract: default 20,
maximum 100, no changing work filters during continuation. Passive inspection
views expose equivalent read operations at their captured prefix.
Cursor validation includes the collection, so a report cursor cannot be used to
page findings. The initial progress read returns a bounded findings page and a
continuation cursor at the same prefix, never an unbounded current-finding list.

The model-facing surface is:

| Tool | Caller | Contract |
| --- | --- | --- |
| `create_agent(role=researcher)` | Root | Create idle researcher; no assignment yet |
| `assign_work(kind=research)` | Root | Assign bounded investigation |
| `update_plan` | Root | Create/revise shared todo structure; retain reservation rules |
| `get_work` | Authorized owner/assignee | Read assignment definition, lifecycle, revision, outcome references |
| `report_work_progress` | Current worker | Record progress/findings and eligible assigned step changes |
| `get_work_progress` | Authorized owner/assignee | Current snapshot, exact report/finding/execution evidence, or bounded history via explicit selectors |
| `get_research_brief` | Authorized owner/assignee | Read a delivered immutable brief by ID with bounded continuation |
| `inspect_agent` | Root | Read runtime state and assignment references |
| `submit_implementation` | Implementor | Proposed rename of existing `submit_work`; original/repair delivery for audit |
| `submit_research` | Researcher | Deliver research brief |
| `submit_audit` | Auditor | Record verdict with existing acceptance/change-request effects |
| `send_message` | Existing authorized callers | Request specific follow-up asynchronously |
| `wait_for_input` | Root and researcher | Working name: end this cycle and return to inbox without final reply or work completion |

The submission rename and wait-tool spelling are recommendations, not settled
contracts (section 12). `submit_work` exists today and is implementation-specific;
this document does not pretend it already accepts research outcomes.

### Exact model read contracts (audit concern 1)

Expose the typed readers through explicit, mutually exclusive tool branches.
These examples are complete selector shapes; model calls use exactly one shape.
`max_bytes` is optional on initial reads only, within the host response budget.

```json
{"mode":"current","work_id":"work-7"}
{"mode":"report","report_id":"report-12"}
{"mode":"reports","work_id":"work-7","limit":20}
{"mode":"finding","finding_id":"finding-9"}
{"mode":"findings","work_id":"work-7","limit":20}
{"mode":"evidence","evidence_ref":"opaque-host-execution-reference"}
{"mode":"continue","cursor":"opaque-progress-cursor"}
```

All seven are get_work_progress argument variants. The first reads WorkProgress;
report/finding resolve exact immutable versions; plural modes explicitly start
history/current-findings pagination. Evidence resolves the exact captured execution
record identified by a host-issued reference (§10), without rerunning the command.
Continuation includes only mode and cursor;
its validated cursor binds session, captured prefix, collection, work/record ID,
requesting actor, byte budget, and ordering/offset. It cannot change filters
or switch collections. Every response identifies its work and assignment binding.

The evidence branch uses the same bounded record reader and continuation mode.
Its typed inspection operation accepts an actor and execution reference and returns
canonical execution metadata plus captured output, or an oversized-record descriptor
with a cursor. Register this branch for root and workers authorized to read the work.
Do not rely on inspect_agent: its current transcript adapter truncates each entry
to 4,096 bytes without continuation within an entry. Reading a report or finding
must leave root able to follow its execution reference to every retained byte.

A cursor identifies data; it does not grant access. On every live model read,
including collection and oversized-record continuations, validate cursor integrity
and actor/session binding, then authorize the actor against current accepted work
state. Keep the original data prefix for a consistent page or record. Reassignment
therefore denies the former assignee's continuation even when the cursor itself
is valid. Return ErrInvalid for malformed/mismatched cursors and ErrForbidden for
valid requests without current access; neither path automatically regrants access.
If authority later changes, evaluate it under the same current work visibility
rules. A historical assignment token cannot supply current read authority.

Archived inspection has a separate trusted host/session access boundary. Once
authorized to inspect the archive, its passive view can evaluate historical work
visibility at the selected prefix. Live model tools cannot opt into that boundary
by supplying an old prefix or cursor. Live/archive content parity at a given prefix
does not imply identical access rights after reassignment. Test both collection
and record-chunk cursors obtained before reassignment: live continuation must be
denied afterward, while separately authorized archive inspection stays passive.

Register get_research_brief as a mandatory immutable result reader:

```json
{"brief_id":"brief-3"}
{"cursor":"opaque-brief-cursor"}
```

Those two shapes are exclusive. It returns brief metadata and bounded finding
references with explicit continuation; finding IDs resolve via get_work_progress.
Neither tool invokes a provider, requests refreshed status, or wakes a worker.
A notice spanning multiple work items is fully resolvable even when none of the
work IDs were previously in model context or findings fall beyond the first page.

### Serialized response budgets (audit concern 5)

Count limits alone are insufficient. All model-facing reads in this family have a
16 KiB default / 32 KiB host maximum budget for the complete UTF-8 serialized tool
JSON, including escaping and envelope overhead, as well as the existing item cap.
The caller may request less; reject budgets too small for a minimal envelope.
Submission and progress mutation return compact receipts, never entire accumulated
briefs or report pages. Trusted typed Go readers may return full records; model
adapters must apply the bounded view rather than JSON-serialize them directly.

A response includes returned items, captured prefix, and a next cursor when more
remain. Stop before either budget is exceeded, even if fewer than limit items fit.
One oversized item yields an explicit oversized-record descriptor (identity,
encoded size, cursor) instead of a truncated object or an empty looping page.
Following the cursor streams bounded UTF-8 text fragments of that immutable
record's serialized JSON, with offset/encoding/completion metadata. It advances
monotonically at code-point boundaries and still counts final JSON escaping toward
the response budget. Readers label fragments as incomplete until reassembled;
record bytes/evidence are neither dropped nor rewritten. The same rule covers a
large single position, many scoped steps, a report, brief metadata, or captured
execution evidence. Execution evidence continuations preserve capture-limit and
outcome metadata; complete pagination does not imply complete command output.

Extend typed inspection with an authorized bounded record-chunk reader used by
these continuation branches. Its opaque cursor pins the original record or derived
snapshot prefix. It resolves canonical recorded content; it is not an arbitrary
file/URL reader. Test worst-case escaping, a maximum-sized report, multiple large
reports, a brief assembled from many reports, and a single oversized field.

`send_message` remains the v1 follow-up path. There is no automatic status polling
or separate refresh-request protocol. Ask an active worker to answer a concrete
question through `report_work_progress`; new investigation after delivery requires
new work. Getting recorded progress never requests another model call.

Reuse `ErrInvalid`, `ErrForbidden`, `ErrNotFound`, `ErrConflict`, and `ErrState`.
Strict schema decoding applies equally to model tools and HTTP commands. Runtime
role policy and domain validation apply to direct Go callers as well.

## 6. Notifications: remember every report, wake selectively

Add `work.WorkProgressReported` and `work.ResearchDelivered` events. Extend `work.Change`
with reports, findings, and briefs so all readers reconstruct the same values.
Update event codec control headers, validation, cloning, and passive read models.

The workflow dispatcher, not the TUI or projector, applies notification policy:

```go
// harness.Config.WorkProgressReporting; cloned into the workflow policy.
type WorkProgressReportingConfig struct {
    BatchWindow         time.Duration // proposed default: 2 seconds
    MinInterval         time.Duration // ordinary notices: 15 seconds
    MaxReportsPerNotice int           // proposed default: 16
}
```

Reject negative durations or nonpositive batch limits. Apply this policy only to
the new progress/report-delivery event kinds; bypass the existing generic work-event
delivery for those events so each change does not generate two notifications.
The batch limit counts all report, brief, and coverage references together. Notices
also obey the serialized byte cap; split large batches without losing coverage or
allowing ordinary notices to overtake an accepted coverage transition.

- Intent/activity-only reports update inspection without a root inbox message.
- New/corrected/retracted findings schedule an owner notification.
- Changed blocker/decision request, delivery, cancellation, and execution failure
  bypass the normal delay. Identical repeated fields do not create new alerts.
- Proposed v1 defaults: a two-second batching window and at most one ordinary
  notification per owner per fifteen seconds. Flush pending developments when
  eligible even if no new report arrives. These are policy settings, not model
  instructions or blocking sleeps.
- One queued notification carries a bounded batch of report references. Mark only
  those reports delivered to the inbox; reports arriving later remain pending.
  Do not rewrite already-recorded inbox messages to add newer reports.
  Multiple ordinary batches drain on eligible windows while their binding remains
  current. Outcome/reassignment coverage retires superseded wakeups as below;
  retaining report history never requires replaying obsolete wakeups.
- Notifications use `message.Notification`, preserving the active request's
  reply target. They are information, not delegated authority or a new user task.
- Keep report delivery to inbox distinct from receipt consumption and user-facing
  narration. None of those guarantees the root has verified a claim.

Proposed typed envelope extension in `message` (which already imports `work`):

```go
type ProgressReportRef struct {
    WorkID             work.ID
    AssignedAtRevision work.Revision
    WorkRevision       work.Revision
    ReportID           work.ProgressReportID
}
type ResearchBriefRef struct {
    WorkID             work.ID
    AssignedAtRevision work.Revision
    WorkRevision       work.Revision
    BriefID            work.ResearchBriefID
}
type ProgressCoverage struct {
    WorkID             work.ID
    AssignedAtRevision work.Revision // the retired assignment, on reassignment
    ThroughRevision    work.Revision
}
type WorkProgressNotice struct {
    Reports []ProgressReportRef
    Briefs  []ResearchBriefRef
    Covered []ProgressCoverage // outcome/cancellation/reassignment covers old wakeups
}
// Add optional Progress *WorkProgressNotice to Message and Draft.
```

Allow this envelope only through the host/workflow path as a notification, with
validated ownership. Model `send_message` cannot manufacture it. Report refs and
brief refs are retained in canonical message records. Add a bounded prose hint
if useful; the ledger remains the source of detail.

### Retire obsolete wakeups, retain all reports (audit concern 2)

Retiring a wakeup means that an ordinary report no longer independently starts a
root exchange. Its content remains valid, stored, and readable. Outcome delivery
covers the earlier progress for notification purposes; it neither invalidates
evidence nor means that an implementation has passed audit.

For each work/assignment binding, derive a live dispatch coverage watermark from
every affected work in a complete accepted Change.Works, comparing prior and new
state/binding. Cover ordinary reports through the transition's work revision when
that assignment delivers its outcome, is cancelled, or is reassigned. Inspect
secondary work changes as well as Event.Work; the primary event can name the
original implementation while a repair or audit assignment closes separately.
Reassignment covers only the old binding. This is derived scheduling metadata,
not a second lifecycle store, deletion, or rewrite of report history.

Required cases are implementation Active → NeedsCheck, repair → Closed,
audit → Closed for either verdict, research → Delivered, and cancellation or
reassignment. The shared criterion is that the assignment stops accepting progress.
Do not wait for all dependent work to be accepted: submission ends that worker's
reporting phase while audit/repair may continue. Keep the existing outcome notice
actionable and attach coverage for every affected binding. A large coverage set
may span bounded notices, but scheduling must apply the full accepted transition
before allowing any covered ordinary report to start an exchange.

- Before posting an outcome/cancellation/reassignment notice, retire covered unsent
  ordinary wakeups.
  Include coverage plus the work/binding and immutable brief ref when applicable.
  The root can read all covered history/findings if needed before composing its
  answer; the notice need not embed the entire backlog. Caller-initiated lifecycle
  actions can establish coverage without an unnecessary self-notification.
- Before enqueueing a timer batch, revalidate binding and coverage against the
  latest accepted work state. Drop obsolete wakeup references, retaining their
  recorded reports. One mixed batch may still contain other active work.
- Already-queued envelopes remain unchanged and inspectable. At inbox consumption,
  classify covered ordinary references as informational and acknowledge receipt.
  When admitting a new exchange, if no actionable references or other waking
  messages remain, return to waiting without a provider call. Preserve the original
  message in history and record its disposition with the evaluated log prefix.
- Recheck admission immediately before the first provider call of a new exchange.
  This decision is the linearization point: a covering transition accepted afterward
  does not revoke an admitted exchange. Process arriving messages at normal
  boundaries. Tests must distinguish events before and after admission.
- Classification belongs to an application-supplied inbox policy, leaving the
  agent loop independent of work-domain rules. Default low-level agent behavior
  stays unchanged. Do not turn every arbitrary Notification into a silent message.
- Timers cannot wake the root solely for reports covered by an already-accepted
  outcome/cancellation/reassignment transition. New research after delivery is a
  new binding/work item;
  later consequential corrections require a new explicit work/instruction path,
  not reopening an old timed notification. Current terminal-report rejection remains.

### Exchange admission and tool continuation

Keep model calls and tool batches sequential within each agent. Asynchronous tool
execution or overlapping root responses are not required for notice filtering;
they would add result ordering, cancellation, and history-pairing obligations.
Other agents already run independently while this agent waits or executes.

The generic agent loop owns the distinction between waiting for a new exchange
and continuing an admitted exchange. Workflow owns the interpretation of work
notices and coverage, returning only a generic admission/disposition decision.
The agent package must not inspect work kinds, states, report IDs, or acceptance
dependencies. Default callers retain existing message-kind behavior. Policy cannot
rewrite envelopes, reorder messages, or redirect replies. Denied admission must
return through normal inbox waiting without losing input queued during that handoff.

Once admitted, an exchange continues after every ordinary tool batch, including
tool errors, without requiring a fresh waking message. Coverage of its original
trigger cannot suppress that continuation. End the exchange only through existing
final-reply behavior, successful explicit inbox yield, or authoritative runtime
control/failure. Pause retains existing resume semantics. A successful yield
returns to new-exchange admission; it does not leave a continuation owed.

Admission grants execution, not work authority. Every tool still checks current
permissions, assignment binding, and revision. Cancellation or reassignment may
make a tool fail normally; the model must receive and process that error. Notice
classification cannot cancel tools, grant permissions, reorder tool results, or
discard their history. Newly consumed notices during an exchange remain available
to its continuation without authorizing a second concurrent exchange.

Test stale-only idle input (no generation), a mixed stale/actionable batch (one
exchange), and coverage arriving after admission or during a tool (normal result
continuation). Include a root tool that itself cancels the triggering work, ordinary
tool errors, reassignment revocation, explicit yield with already-queued input,
and silent observations. Verify history/result pairing and unchanged default
low-level agent behavior as well as provider-call counts.

Replay reconstructs coverage and scheduling dispositions passively. It never runs
live admission policy, starts an inbox loop/timer, or calls a provider. Required
tests cover outcome delivery/cancellation/reassignment
before timer expiry, during root generation, and after enqueue but before consumption;
include multi-work batches and final-root-answer-before-old-timer cases.

Use stable originating event IDs to avoid duplicate dispatch within a live
session, following existing pending-event acknowledgment. Do not claim exactly
once delivery across process restarts; execution recovery is outside v1. Reading
or replaying an archive never schedules notifications. Stop timers on shutdown;
paused agents remain paused and stopped agents are not revived for a summary.
The common report path covers all worker roles; existing audit verdict and work
submission notifications keep their effects. New pure step/activity reports stay
in the ledger without a root inbox message. This intentionally replaces the old
automatic observation forwarding for that path to limit root context growth.
Findings/attention use the bounded wake policy; never send both notice paths for
the same originating event. Unrelated observations retain their current semantics.

Root generation is serialized. User input never cancels an in-flight call merely
because it arrives. At the next normal inbox boundary the root must consider new
user steering before deciding whether background findings still deserve narration.
Suppress unnecessary new background wakeups while user input is already pending;
do not reorder or rewrite recorded history to implement priority.

## 7. Commentary and waiting without premature completion

Keep free-form commentary as authored text. Do not replace `CommentaryEvent` with
JSON or derive findings from reasoning. Workers report facts through `report_work_progress`;
the root publishes human-facing commentary through the existing output track.

`wait_for_input` is the proposed clearer name for the earlier `yield_control`.
Its contract is return to the inbox: both user input and actionable background
messages qualify. It does not hand control to a named worker, block the session,
complete an assignment, or start polling. The final spelling remains open.

Root flow:

1. Read delivered report references/current work state.
2. If meaningful, emit one or two grounded sentences alongside `wait_for_input`.
3. Yield to inbox processing without publishing `message.Reply` or changing work.
4. On research delivery, synthesize the answer or decide on further work. A final
   text-only response retains existing reply semantics.

Proposed runtime extension in `tool`:

```go
type ControlKind string
const YieldToInbox ControlKind = "yield_to_inbox"

type ControlTool interface {
    Tool
    Control() ControlKind
}
```

Root and researcher receive `wait_for_input` as an explicitly registered ControlTool.
Role assembly grants this capability; do not infer it from work status. The agent loop
checks trusted tool capability, not a hard-coded model-generated tool name. Tool
preparation/snapshotting must preserve this capability. Ordinary tool results or
text cannot request a control action.

Contract: a control tool must be the sole call in a batch. Preflight this before
executing any tool in that batch. On a mixed batch, append matching error results
for all requested calls without side effects and let the model correct itself.
On successful yield, append the normal tool result and batch boundary, record an
agent yield event correlated to OutputID, then return to the outer inbox loop.
Failure follows normal tool-error handling and does not yield.

No additional provider generation is needed to acknowledge successful yield.
Already-queued actionable messages must be consumed without a lost wakeup;
observations alone retain their existing non-waking semantics. Pause, stop, and
session shutdown remain authoritative. The root can be idle while owned work is
active; that means waiting, not task completion. The same applies to a researcher
with an open assignment. TUI status should distinguish runtime waiting from a
reported blocker or a delivered outcome.

Researcher flow when external intervention is required:

1. Report the concrete blocker/decision and supporting evidence. Request specific
   help through send_message or an actionable DecisionNeed; a dependency-only
   informational report cannot wake the root. Observe a successful receipt before
   relying on delivery; on an error, correct the request or report the failure.
2. Continue any independent investigation. If none remains, call wait_for_input
   as a separate sole-call batch. Do not poll or send a final reply to simulate
   waiting. Yielding does not submit a brief or change assignment lifecycle.
3. Root explicitly calls send_message to the researcher with the correlated answer
   or instruction, which resumes processing through normal inbox admission. Root's
   ordinary final reply goes to the user and does not wake the researcher.
   Inspect current assignment authority before further work; the root may have
   cancelled or reassigned it. Correlate the reply with the outstanding request;
   receipt alone is not evidence that a dependency has been fulfilled.
4. Report the changed position and clear a resolved blocker explicitly, then
   continue investigation or deliver the brief through submit_research.

This is explicit agent inbox yielding, not an automatic dependency scheduler.
It does not require an execution helper, grant delegation authority, or broaden
researcher shell limits. Test help arriving before yield and after the researcher
has entered waiting, unrelated input, revoked assignments, request failure, and
absence of a synthetic completion reply. No provider call is needed merely to
acknowledge successful yield.

`submit_research` remains a normal tool. The worker can give its existing final
reply afterward, but the root should avoid repeating a delivered brief when that
reply arrives. Duplicate narration is addressed by stable brief/report IDs and
root context, not by claiming model output is exactly once.

## 8. User-facing contracts and limits

- The root owns normal user-facing progress. Worker reports remain inspectable
  and can feed a folded activity view without becoming duplicate root messages.
- Opening a disclosure, reading status, resizing, or replaying history causes no
  model calls, inbox writes, or execution changes.
- The root distinguishes observed facts, inference, intent, and uncertainty.
  It does not turn a file read into a discovery or an untested concern into proof.
- Summary evidence is traceable through report/finding IDs. The root need not
  re-read every source, but must inspect evidence before claiming independent
  verification. Corrections should be surfaced if an earlier claim was narrated.
- Reports express no automatic percentage complete. Provider silence does not
  imply stalled work. Runtime telemetry and semantic status remain separate.
- Keep detailed logs complete; bound notification payloads and inspection pages,
  not the recorded findings. Do not stream every report into root context.

The view-only compression proposal remains separate from reporting semantics.
One stable disclosure may group an agent's thinking and tools for a response;
root commentary, final replies, errors, and requests for input remain visible.
The disclosure exists from the beginning and keeps its explicit expansion state
as more calls arrive. Expansion restores the original chronology and identities.
Never collapse text because a new model call started. Grouping interleaved agents
must preserve chronological segments rather than silently reorder the timeline.
Current context measurements are snapshots and are not summed across calls.

## 9. Taxonomy migration and compatibility

The original `update_plan` design intentionally doubled as shared todo progress.
That storage boundary is sound. The revision clarifies the model surface as work
gains evidence-bearing reports, including research without any plan scope.

| Existing/proposed-old surface | Revised destination | Preserved behavior |
| --- | --- | --- |
| Implementor `update_plan(work_id, steps...)` | `report_work_progress` | Harness immediately applies eligible assigned step status/note changes |
| Auditor `update_work(note, blocker)` | `report_work_progress` | Reports do not constitute audit verdicts |
| Root `update_plan(plan_id, ...)` | Retain `update_plan` | Root owns plan structure and must respect reserved steps |
| Proposed `report_status` | Replace with `report_work_progress` | Structured work reports; no agent lifecycle authority |
| Proposed `get_research` / status umbrella | Replace with `get_work_progress` and explicit outcome reads | Passive progress inspection with bounded detail |
| Existing `submit_work` | Retain `submit_work` | Implementation/repair delivery still requests audit |
| Earlier `yield_control` | Selected `wait_for_input` | Return to inbox, no final reply or work completion |

### Explicit progress API break (audit concern 6)

Do not advertise synonymous old and new names together to models. This revision
chooses a deliberate mutation-API break, not a legacy patch adapter:

- Remove worker progress branches from update_plan and remove worker update_work.
  Reject old wire requests with a migration error; they must not mutate state.
- Replace the legacy direct-Go/HTTP progress mutation with the new required
  assignment-bound contract. Callers migrate in the same release. Never fetch the
  latest assigned-at revision on behalf of an old execution or invent an objective
  to convert a note-only patch into a complete reported position.
- New callers either omit Position entirely (step/finding-only change) or send an
  intentional full position using an inspected snapshot and matching revision.
  Validation/CAS prevents stale writes; omission never silently clears a blocker.
- Preserve canonical historical records and old recorded tool names. Passive
  readers still understand old ProgressChanged changes. Where legacy history lacks
  structured position, expose its note/blocker as legacy recorded metadata with
  known provenance, not fabricated researcher-authored fields.
- Update tool schemas, role prompts, host/HTTP adapters, tests and release notes
  together. Preserve root plan creation/edit requests and existing audit outcome
  semantics. Submission renaming remains an independent naming decision.

This avoids an underspecified adapter clearing blockers or weakening assignment
fences. The current root/worker distinction in plan/work revision counters remains.

Worker progress and plan status must stay synchronized in the harness. A root
model call is needed only for a decision, structural plan change, or narration.
Research can recommend new tasks; it cannot manufacture implementation scope,
mark steps completed, or bypass the audit acceptance path.

Do not add a private todo ledger in v1. If workers later need personal checklists
below an assigned shared step, evaluate that as a separate ownership/granularity
requirement. It is not needed to introduce research or improve conversation quality.

## 10. Bounded researcher execution

Recommend direct bounded execution for ordinary diagnostic checks in v1. The
researcher already has the question, relevant files, and evidence context. A second
model merely forwarding commands adds latency, another handoff, and dependencies
without improving the command timeout mechanism. Root remains responsive because
each agent has its own loop; the researcher itself waits synchronously for the call.

### Existing capability and proposed limits

[tool.Shell](../../tool/shell.go) already supports per-call timeouts, a host maximum,
output limits, selected environment variables, and process-group cleanup. Defaults
are 30 seconds, a 5-minute maximum, and 64 KiB retained output. It explicitly runs
with host permissions: the working directory is not a sandbox. The
[process-group mechanism](../../tool/process_unix.go) does not contain descendants
that deliberately escape the group.

Give the researcher a separately configured shell instance, not the shared role's
unmodified tool list. Proposed host-selected diagnostic limits:

```go
type ResearchExecutionConfig struct {
    Enabled       bool
    Timeout       time.Duration // default 30 seconds
    MaxTimeout    time.Duration // proposed cap 60 seconds
    OutputLimit   int           // proposed retained preview 16 KiB
    Env           []string     // explicit narrow environment; cloned configuration
}
// harness.Config.ResearchExecution; consumes existing tool.ShellConfig underneath.
```

The tool remains `shell`; provider schemas enforce the host maximum timeout. A
model can request less time, never raise the cap. One synchronous command runs per
researcher at a time, with no persistent shell/background jobs. The root uses
existing agent control to stop execution; pause remains a checkpoint operation
and need not instantly interrupt the command. Stop/session cancellation must reach
the command's process group, with bounded pipe draining and truthful cleanup errors.

### Explicit assignment selection for diagnostics

An agent can hold multiple active assignments, and its inbox can contain several
assignment instructions before one generation. Preserve that existing capability.
Neither the latest inbox message nor the executing actor alone identifies which
work a command serves. Do not infer an assignment from either, even when an agent
currently happens to have only one active work item.

The workflow-owned researcher diagnostic wrapper exposes `shell` with required
assignment selectors in addition to the underlying command arguments:

```go
// Workflow adapter arguments; not fields on the generic shell implementation.
type ResearchDiagnosticArgs struct {
    WorkID             work.ID       `json:"work_id"`
    AssignedAtRevision work.Revision `json:"assigned_at_revision"`
    Command            string        `json:"command"`
    TimeoutMS          *int64        `json:"timeout_ms,omitempty"`
}
```

These selectors are untrusted model requests. Immediately before admitting
execution, workflow validates the bound runtime actor, active research work,
current assignee, and exact assigned-at revision, then captures canonical
attribution from that validated state. Order validation and capture as one
decision against assignment transitions. Reject missing, unauthorized, terminal,
or stale selections before invoking the shell; never fill in the current token
for a stale caller or silently switch to another assignment. Ordinary progress
revision changes do not invalidate this selector; reassignment does, including
reassignment back to the same actor.

After admission, pass only command/timeout to the configured generic shell and
retain the validated work binding in host execution metadata. Release assignment
locks before running the command. Later cancellation or reassignment cannot
relabel an admitted execution; explicit agent stop/session cancellation retains
its existing behavior, and any subsequent report must pass current mutation
authority checks. This admission decision does not grant ongoing work authority.

Test one researcher with active work A and B executing for A after receiving B's
instruction; its evidence must remain attributed to A. Also test missing selectors,
another actor's work, reassignment before admission, reassignment back to the same
actor, ordinary progress between generation and execution, and a transition after
admission. Denied calls execute no command; admitted calls never change attribution.

These are time/output/process-lifetime bounds, not CPU/memory/disk/network quotas
or a security sandbox. Build and test commands can execute project code, write
caches/generated artifacts, and have other side effects. Omitting edit/write tools
and instructing diagnostics-only use is a role policy, not enforced read-only
execution. Expose that accurately in effective configuration and tool descriptions.
A deployment requiring strict isolation must provide an appropriate execution
boundary; do not claim a command-prefix allowlist or working directory supplies it.

Research prompts permit discovery/build/test diagnostics within the assignment's
authorization and forbid using the shell to perform repairs or unrelated mutations.
They request an initial purpose-driven progress report and meaningful findings
rather than narrating every command. A timed-out check is a result to interpret,
not automatic blocked work, audit failure, or evidence that the project cannot run.
Do not silently retry the same timeout forever or bypass limits using backgrounding;
report the limitation and ask the root when a longer/broader operation is necessary.

### Result and evidence contract

Retain exit status when available, timed-out/cancelled outcome, truncation and
incomplete-output flags, command identity, start/end times, and inspectable evidence
reference. Distinguish start failure, nonzero exit, timeout, cancellation, and
cleanup failure. Tie a report's evidence to the actual tool invocation.

### Issuing and resolving execution evidence

Introduce a host-issued opaque execution reference, returned as `evidence_ref`
alongside the model-visible diagnostic result. A finding copies that exact value
into EvidenceRef.URI and uses Detail/Locator to explain what it supports. The model
does not construct references from guessed invocation IDs, paths, or tool-call IDs.

The reference resolves to a session and an accepted canonical tool-finish record,
including the host invocation identity, executing actor, and the work/assignment
binding captured at execution admission. Invocation identity and executing actor
are host-supplied, never model arguments. The model selects intended work through
the diagnostic wrapper; workflow validates those selectors and supplies canonical
assignment attribution from accepted state. Carry generic invocation identity
through tool execution. Keep work-domain interpretation
outside the generic agent loop and shell implementation. Persist the attribution
with the execution records so passive archive reconstruction needs no live map.

Publish the required finish record successfully before exposing a resolvable
reference to the model. The recorded bounded result and the model response identify
the same invocation. Recording failure returns no successful evidence receipt.
Normal results and nonfatal tool errors retain the reference and outcome metadata;
the existing generic error-string path must not discard that structured evidence.
If cancellation prevents another model turn, retain the finish record and its
reference for authorized host inspection through existing tool-event discovery. No worker
report is required to preserve the execution record, and cancellation cannot be
advertised as successful execution.

The §5 evidence selector resolves that reference to the recorded command identity,
start/end times, outcome/error, exit status when available, capture flags/limits,
and retained output. Reuse the bounded record-chunk reader for oversized records.
The reference is a locator, not a bearer permission or arbitrary URL/file reader.
Validate the session, invocation, accepted record, and attributed work before use;
unknown references fail, and forbidden references reveal no execution content.
Current work visibility authorizes every live initial read and continuation;
archived inspection uses the separate §5 host access boundary.

When accepting a report, resolve execution references against canonical host
records and require the same work ID. For newly captured evidence, the recorded
actor/assignment binding must match the reporting assignment. A new assignee may
cite prior-assignment evidence from the same work as inherited evidence, preserving
its original actor/binding; it must not be relabeled as the new assignee's execution.
References to another work item are rejected in v1 even if their spelling is known;
root may forward an authorized excerpt with source attribution as described in §10.
Perform validation through host-supplied recorded-evidence lookup, with the report
commit retaining its normal current assignment/revision checks. Do not make the
work store depend on the shell, fetch URLs, or rerun commands to validate evidence.

Acceptance: researcher runs a diagnostic whose decisive retained error is beyond
byte 4,096, reports a finding using the returned reference, and root follows that
reference through bounded pages to the exact recorded error without another shell
or provider call from the reader. Also test escaped output, error/cancelled records,
publication failure, forged/cross-work references, inherited attribution, reassignment
between evidence pages, and passive archive resolution. Preserve the distinction
between all retained output and bytes discarded by the shell's capture limit.

Existing ShellResult already includes output, exit_code, timed_out, truncated and
output_incomplete. Current parent-context cancellation returns an error without its
partial result; strengthen this path before claiming partial evidence survives
cancellation. Preserve the bounded captured result in the tool event alongside the
error, even when cancellation prevents another model-history turn. Do not claim
complete command output when the tool only retained a head/tail preview. The log
retains all accepted reports and captured evidence, including explicit capture
limits, not bytes the shell intentionally discarded.

Model-facing shell JSON must also fit the final serialized response budget; adjust
preview retention to account for escaping/metadata or use the bounded record-reader
path. The 16 KiB raw preview cap alone is not a 16 KiB serialized-JSON guarantee.
Test stop/timeout, ordinary descendants, inherited pipes, partial output, escaping,
start failure and cleanup errors. Process-group cleanup is best effort within its
stated boundary, not a guarantee against deliberate process escape.

### When delegation remains appropriate

Use an implementor for code changes, with repair assignments reserved for the
failed-audit path. Use an auditor for a completed implementation submission ready
for verification. Additional investigation belongs to research work. The root owns
these decisions and creation/assignment. Researchers do not create helper agents
in v1. The root must not disguise a one-command request as accepted implementation;
any future lightweight execution-work kind needs its own delivery contract.

If external help is needed, researcher sends an explicit `send_message` request to
the root (or an actionable DecisionNeed), then explicitly yields under §7 if no
independent investigation remains. A dependency-only report
is informational and is not sufficient to wake an idle root. Treat a dependency as
underway only after the root has actually arranged it. Root forwards correlated
results/evidence under existing access rules; a work-ID reference grants no access.

Waiting for a known result, blocked on intervention, and continuing independent
work remain distinct. A synchronous bounded shell call needs no new dependency
work item and uses its normal tool-result continuation. Waiting for external help
uses the same trusted inbox-yield capability as root. A generic execution-work kind
and automatic dependency scheduling remain deferred.

## 11. Implementation order and acceptance gates

1. **Progress vocabulary and existing behavior:** consolidate worker progress
   tools, retain root plan structure, and prove unchanged scoped-step/audit authority.
2. **Reports and reconstruction:** immutable progress findings, current position,
   revision/assignment fences, complete event changes, live/archive parity.
3. **Research role and outcome:** registration/configuration, assignment contract,
   Delivered lifecycle, brief submission, authorization, list/inspection updates.
4. **Delivery policy:** typed notices, batching, urgent changes, pending delivery,
   shutdown behavior, deterministic tests with an injected clock.
5. **Exchange admission and root/researcher yielding:** explicit tool capability
   and output correlation, preserved ordinary tool continuation, history pairing,
   inbox races, pause/stop, and no unintended final reply.
6. **Prompts and presentation:** evidence-based reporting examples, root synthesis,
   worker detail, waiting status, and preserved full-detail inspection.
7. **Behavioral evaluation:** scripted provider coverage followed by a bounded
   real-model PLAN.md comparison. Record latency and extra root calls as well as
   summary fidelity and usefulness; do not infer quality from type validation.

Configure and test direct researcher diagnostics as part of role assembly,
including cancellation evidence and serialized output bounds. A separate generic
execution-work kind and automatic dependency scheduling remain deferred; they
are not prerequisites for ordinary bounded build/test checks.

Required checks include:

- Implementor progress updates the authoritative scoped plan step immediately,
  without root generation; plan structure and completion remain protected.
- Step-only reports work; changing position preserves accumulated findings.
- Auditor progress is not a verdict; research proposals do not alter plan steps.
- Progress reads expose current work revision/binding and distinguish historical
  findings from current position; no separate step-status ledger diverges.
- Mutations carrying an old assignment binding are rejected after reassignment,
  including reassignment back to the same actor.
- Live pagination and record-chunk reads recheck current permission while keeping
  the data prefix; no cursor grants or restores authority. Historical inspection
  requires its separate host access boundary.
- Reports racing cancellation/submission cannot overwrite terminal state.
- Position updates preserve findings; corrections/retractions retain history.
- A stale duplicate report adds no second finding; uncertain callers can inspect.
- Recording failure returns no success and produces no unrecorded root notice.
- Live and reopened JSONL agree at the same prefix; replay dispatches nothing.
- Notification batching retains every referenced development, including arrivals
  during root generation; consumed receipts alone do not imply narration.
- Yield completes tool history and waits; pending user/notification messages wake
  correctly; silent observations, failures, mixed batches, and shutdown are covered.
- Ordinary tool continuation survives coverage of its triggering notice; admission
  policy cannot cancel execution or bypass current mutation authority.
- Researcher help requested before yield resumes correctly whether the reply arrives
  before or after waiting; assignment remains open without a fake completion reply.
- A delivered brief is not audited acceptance; proposed steps never mutate a plan.
- An integration fixture reports an observed scoped failure plus uncertainty;
  the root communicates it before research ends without claiming broader proof.
- Direct diagnostic timeout/cancellation retains truthful outcome
  and partial evidence; waiting does not become blocked or complete automatically.
- Diagnostic results issue exact host references; root resolves captured evidence
  beyond transcript truncation through bounded authorized reads, without execution.

### Seven audit findings: resolution and required proof

| Concern | Contract resolution | Acceptance check |
| --- | --- | --- |
| 1. Unresolvable notices | Typed work/binding/report refs; exact report/history/finding modes; mandatory brief reader (§5–6) | Start from a notice spanning unfamiliar work items; resolve exact reports and a brief beyond initial pages |
| 2. Stale wakeups after delivery | Complete accepted Change.Works drives outcome/cancellation/reassignment coverage; inbox admission classifies queued stale references (§6) | Implementation submission, repair submission, either audit verdict, research delivery, cancellation and reassignment before timer expiry and after enqueue; no new exchange solely for covered reports, existing tool continuation preserved |
| 3. Research falls through audit lifecycle | Exhaustive kind switches; explicit research cancellation; Delivered is terminal (§2) | No empty-parent work, review event, or plan mutation; terminal report/reassign/cancel rejected |
| 4. Historical blocker looks current | Authoritative WorkStateSnapshot plus separately timestamped LastReportedPosition (§4) | Delivery/cancellation clears active blocker while preserving report; step-only update does not freshen old position; reassignment clears current note |
| 5. Unbounded model responses | Serialized byte budgets, compact receipts, bounded brief references, oversized-record continuation (§4–5) | Worst-case escaping and large report/brief fixtures stay within response cap and reconstruct complete captured records |
| 6. Lossy legacy conversion | Explicit mutation API break; old patches/tokens rejected, historical decoding retained (§9) | Legacy note/blocker calls cannot clear state; no fresh assignment token inferred for stale callers |
| 7. Terminal work loses scoped steps | Read exact scoped IDs through work authority at a captured prefix, with explicit missing IDs (§4) | Completed/cancelled scoped reads succeed; unrelated steps denied; removed steps identified |

These are design resolutions, not claims that runtime tests have already passed.

The follow-up audit additionally requires the §6 exchange-admission fixtures,
§7 researcher escalation/yield fixtures, and §5 permission revocation on both
collection and oversized-record cursors. These checks cover runtime control and
access boundaries without changing the agreed implementation/audit/repair sequence.
The latest evidence audit adds the §10 producer-to-reader fixture: host-issued
diagnostic reference, report validation, exact model resolution, and bounded access
to all retained output. Researcher resumption explicitly requires root send_message.
The final attribution audit requires explicit diagnostic work/assignment selectors
and the §10 multi-assignment and reassignment fixtures. Host-authored attribution
is derived from validated selectors, never guessed from the latest assignment.

## 12. Remaining decisions and audit conclusions

Agreed direction: preserve the shared plan/todo model; use one work-progress
vocabulary across roles; retain direct authorized worker step updates; add a single
researcher/planner role; let the harness remember evidence and the root interpret
it. No separate mutable root-memory ledger or research-specific progress idiom.

The three taxonomy auditors independently recommended `report_work_progress` and
`get_work_progress`, separating runtime inspection, assignment definition, ongoing
reports, and delivery. They also agreed to consolidate the existing progress paths
rather than expose additional aliases. Final submission naming did not have consensus.

| Decision | Current recommendation | What remains open |
| --- | --- | --- |
| Delivery names | `submit_implementation`, `submit_research`, `submit_audit`, one appropriate tool per role | Generalized `submit_work` with typed results is an alternative; never silently change current semantics |
| Wait tool spelling | `wait_for_input` with explicit user/background inbox contract | `yield_control` or another name can change; semantics must not |
| Researcher execution | Direct bounded diagnostics using a role-configured shell | Tune limits and deployment isolation; never call timeout/output caps a read-only sandbox |
| Worker waiting | Researcher explicitly yields to inbox when awaiting external help; synchronous diagnostics retain ordinary tool continuation | Generic execution-work kinds and automatic dependency scheduling remain deferred |
| Notification cadence | Batched ordinary findings; immediate attention changes | Tune proposed timings against real model latency and usefulness |

These choices are not permission requests or assertions that the feature is built.
The document separates them so implementation cannot accidentally broaden authority
or change completion behavior under cover of a tool rename.

## 13. Revision summary

- Preserved plans as the shared todo list and clarified that worker reports can
  directly update assigned step status through the harness.
- Replaced proposed research/status-specific progress tools and types with a
  shared work-progress family; kept assignment and runtime inspection distinct.
- Added the existing audit-finding type boundary and general progress findings.
- Added optional step-only reporting, cumulative evidence, and bounded progress reads.
- Incorporated the audit consensus and marked submission naming disagreement.
- Chose bounded direct researcher diagnostics over a mandatory executor-agent hop;
  documented existing shell controls, their limits, and cancellation-evidence work.
- Resolved the seven audit findings with exact reads, byte budgets, terminal
  notification coverage, explicit lifecycle branches, truthful snapshots, a clean
  progress API break, and narrowly scoped terminal reads.
- Preserved root-owned delegation for broader work; added explicit researcher
  inbox yielding and deferred generic execution work items/automatic scheduling.
- Retire ordinary wakeups at assignment outcome delivery, cancellation, or
  reassignment across complete accepted work changes; retain every report.
- Keep sequential tool/model execution and separate new-exchange admission from
  owed tool continuation, with work-domain policy outside the generic agent loop.
- Recheck live cursor permissions against current authority while retaining the
  original data prefix; historical inspection has a separate access boundary.
- Added host-issued execution evidence references and exact bounded model reads,
  with assignment attribution, publication guarantees, and inherited-evidence rules.
- Required explicit work/assignment selection in the workflow diagnostic wrapper,
  validated before execution while preserving multiple assignments per agent.
- Clarified that root explicitly messages a waiting researcher; its user-facing
  final reply is not an agent-to-agent handoff.
- Made each role/work kind's required input, delivered outcome, and root-assigned
  implementation → audit → repair → audit sequence explicit. Research-discovered
  defects enter implementation work unless an applicable failed audit already exists.
- Retained view-only folding, recorded evidence, selective wakeups, and live/archive
  reconstruction without execution effects.
