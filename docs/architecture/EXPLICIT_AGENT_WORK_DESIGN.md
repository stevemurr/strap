# Explicit agent creation and work assignment

Status: implemented. Explicit role-based creation, required-assignee assignment and
reassignment, explicit repair assignment, recorded role views, and fixed-prefix
work discovery are implemented together. The sections below retain the reviewed
contract and intended sequencing; both stages are now complete. Assignment tool
names and shared decoding were updated on September 16, 2026 by the
[schema architecture fix](MODEL_TOOL_SCHEMA_REVIEW.md). Earlier validation results
below describe the interface that existed at the time.

Implementation also addressed issues found during verification:

- Composed schemas no longer advertise internal branch names as titles, and runtime
  validation errors label argument variants without suggesting additional tools.
  A live model had tried to invoke the internal `assign_repair` branch.
- Field descriptions and root guidance distinguish new implementation assignments,
  repair inputs derived by the server, and transfer of existing work to a replacement.
- Agents remain idle while initially waiting for an inbox message; startup no longer
  emits a transient running state before any work arrives.
- Cursor decoding starts from an empty value, preserving filters across pages without
  accidentally retaining the incoming continuation token.

Validation on 2026-09-12: the complete race suite and deterministic tool/Go/HTTP
lifecycle tests cover the implementation. The real-model arithmetic example reached
an accepted audit. The final `nemotron-lightning` discovery run passed creation,
reuse, audit, repair, stopped-worker replacement, and worker escalation, with zero
rejected calls in that run. Earlier runs exposed schema/name and cross-variant field
confusion; this single successful run is not a reliability estimate.

The opt-in [discovery evaluation](../../harness/agent_discovery_eval_test.go) uses a
real model for the actor under evaluation and idle fixtures for collaborators.
It logs rejected calls separately from outcomes; `STRAP_EVAL_STRICT=1` requires
zero rejected calls. The independent `examples/local` run exercises actual model
implementation and auditing. Run discovery with:

```sh
STRAP_EVAL_URL=http://127.0.0.1:1234 STRAP_EVAL_MODEL=your-model \
  go test ./harness -run '^TestAgentDiscoveryLive$' -parallel 3 -v
```

## 1. What the user and model should understand

“Create an agent and have it do X, Y, and Z” maps to two operations:

```javascript
const worker = create_agent({role: "implementor"});
const task = assign_implementation({
  assignee: worker.agent_id,
  task: "Do X, Y, and Z.",
  expected_output: "Completed changes and verification evidence."
});
```

An **agent** is a persistent execution identity with a role and lifecycle. A
**work item** is a tracked assignment with an assignee, requirements, revisions,
and an outcome. The role belongs to the agent. The work kind belongs to the task.
Assignment validates that the agent's role can perform that kind of work.

The intended contracts are:

- `create_agent` creates an idle, registered agent. It does not assign a task or
  initiate a model request. Registration means workflow eligibility is established.
- The four operation-specific assignment tools create a work item and schedules its delivery to a required,
  existing assignee. It never creates an agent.
- `reassign_work` changes an active work item's assignee to a required, existing
  agent. It never creates, resumes, or stops an agent.
- `submit_work` records a submission and requests review. It creates no auditor.
- `submit_audit` records a verdict and its effects on the reviewed work. It creates
  no agent or repair assignment.
- `assign_repair` explicitly creates repair work from a failing audit.
- `list_work` discovers tracked work without requiring a work ID, including work
  that has already been submitted, accepted, closed, or cancelled.

These are mutation tools with declared effects. The objective is to remove
implicit resource creation, not to separate every internal state update into a call.
Plans remain optional. A single task can contain X, Y, and Z without creating a plan.

## 2. Roles and model instructions

The application provides fixed execution configurations. Do not add arbitrary
role names or model-authored system prompts as assignment inputs. Research is
covered in the [research design](RESEARCH_STATUS_DESIGN.md).

| Creation role | Purpose | Eligible work kinds |
|---|---|---|
| `researcher` | Investigate a bounded question and submit an immutable brief | `research` |
| `implementor` | Execute assigned implementation tasks and corrections | `implementation`, `repair` |
| `auditor` | Independently verify a submitted outcome and record findings | `audit` |

`root` is an inspection role established by session bootstrap; it is not an allowed
`create_agent` argument. Only the root can create execution agents and assign or
reassign tracked work. Role selection does not allow the model to grant capabilities.
The application resolves the role to its configured provider, prompt, and tool set.
An agent's role is immutable for its lifetime. Create another agent to change roles.

The `create_agent` description follows this rule:

> Create an idle agent with a configured role. Choose implementor to execute tasks
> or repairs; choose auditor to independently review submitted work. Returns
> agent_id and role. Then call the appropriate assignment tool with that agent_id as assignee to begin
> tracked work. Creation alone does not start a task.

The root instructions in `harness/prompts.go` follow this rule:

> To create or spawn an agent, call create_agent with role implementor for task
> execution or repairs, or auditor for independent review. Then call the appropriate assignment tool
> with the returned agent_id as assignee. You may reuse an existing eligible agent.
> Plans and scope are optional. Put task-specific requirements in the assignment.
> An idle agent is not necessarily free of assigned work; inspect its assignments
> when deciding whether to reuse it.

Include the minimal two-call example above in the root prompt. Add a short role
description to the tool schema's `role` field, as well as its required enum.
Keep the two role descriptions consistent with the role table and configured prompts.

Implementor and auditor instructions must state that they cannot create or assign
agents. If additional help is needed, message the owner from the work snapshot.
If progress depends on that help, record a blocker through the role's progress tool.
Receiving an ordinary message does not change role, grant scope, or create work.

Root review instructions become: read current work, create or reuse an auditor,
assign its audit, and wait for the verdict. On failure, read the immutable audit,
select or create an implementor, and explicitly assign repairs. Do not report
success until a passing audit accepts the outcome.

## 3. Model-facing schemas

`create_agent` has one required argument:

```json
{"role":"implementor"}
```

Its result is a registration, for example:

```json
{"agent_id":"agent-2","parent":"agent-1","role":"implementor"}
```

The agent starts idle. Lifecycle state remains a separately observable snapshot
through list/inspection. No task, scope, provider, tools, parent, or caller identity
is accepted from model arguments. The bound caller becomes the parent.

Each assignment operation has one public tool name and one strict input contract:

| Tool | Required arguments | Optional arguments | Derived by application/store |
|---|---|---|---|
| `assign_implementation` | `assignee`, `task` | `context`, `expected_output`, `scope` | Owner, work ID, initial revision |
| `assign_research` | `assignee`, `task` | `context`, `expected_output` | Owner, work ID, initial revision |
| `assign_audit` | `assignee`, `work_id`, `expected_revision`, `submission_id` | None | Audit task and scope from the selected submission |
| `assign_repair` | `assignee`, `work_id`, `expected_revision`, `audit_id` | None | Repair task, findings, expected output, and affected scope from the audit |

For audit and repair, `work_id` identifies the **original implementation work**.
`expected_revision` is that original work's current revision. `audit_id` is a
verdict record ID, not the audit work item's ID. These distinctions must appear in
the tool description and examples.

All assignment operations require a nonempty assignee; omitted, empty, null,
unknown, and incompatible values fail validation without creating an agent. Repair callers
cannot override findings, task, or scope. Audit callers cannot replace the submitted
requirements with a new task. The tool name supplies the internal work kind;
payloads do not accept `kind`.

### Strict JSON decoding and normalized commands

Assignment tool calls and matching HTTP command bodies use the same declarative
input contracts before converting JSON into the shared Go request. A flat Go struct cannot tell
an omitted field from an explicitly supplied `null` or empty value after ordinary
JSON decoding. `DisallowUnknownFields` alone cannot enforce the branch contract.

The operation types, `Parameters` values, constraints, and normalization
functions live in `tool/work_contract.go`. Both `AssignmentTools` and
the exported `DecodeAssignment` function use those exact operation definitions. Schema
generation and decoding must remain coupled; do not maintain a second handwritten
HTTP schema or run a tool callback merely to validate arguments.

```go
// Pure decoders; no actor checks, handlers, or runtime side effects.
func DecodeAssignment(name string, raw json.RawMessage) (work.AssignmentRequest, error)
func DecodeReassignment(raw json.RawMessage) (work.ReassignRequest, error)
func DecodeAgentCreation(raw json.RawMessage) (roster.CreateRequest, error)
```

The creation and reassignment decoders similarly reuse their tool's compiled
`Parameters` contract. These helpers belong to the tool/adaptation layer; the
workflow and work store do not acquire a dependency on `tool` for domain validation.
Do not add `UnmarshalJSON` methods to these request structs: `tool.Parameters`
deliberately rejects custom codecs when compiling a schema.

Assignment HTTP routes use the operation names (`/work/assign_implementation`,
`/work/assign_research`, `/work/assign_audit`, and `/work/assign_repair`). The old
`/work/assign` route and `assign_work` tool are removed, with no compatibility alias.
The HTTP handler first validates the command envelope and retains `request` as
`json.RawMessage`. It then calls the appropriate pure decoder and passes its
normalized result to the shared harness operation. Missing/null requests, malformed
JSON, unknown envelope fields, and trailing values are invalid. Retain request-size
limits and authorization before execution. Do not decode `request` into the flat
`AssignmentRequest` first, or round-trip it through JSON to perform validation.

Each assignment operation rejects every field not declared for that operation, regardless
of its value. For example, implementation requests with `submission_id: null`,
`submission_id: ""`, or `audit_id: ""` are all invalid; absence is valid. Explicit
null is invalid for optional fields too. Missing required fields, wrong scalar
types, empty required identifiers, and nested unknown fields are invalid on both
assignment wire surfaces. Select the contract by operation name before normalization.

After decoding, the workflow performs the common semantic validation for tools,
HTTP, and direct Go: valid kind, required values, no nonzero incompatible selectors,
authority, role eligibility, and current work/submission/audit relationships. Direct
Go values have no wire-level field-presence distinction; their zero-valued unused
fields mean absent. Promise identical semantics for normalized commands, and
identical JSON acceptance/rejection for matching assignment tool and HTTP operations.
This does not promise schema parity for unrelated HTTP commands, including the
existing audit-verdict HTTP decoder.

`reassign_work` requires `work_id`, `expected_revision`, and `assignee`. It addresses
the actual active work item being moved, including a repair or audit child.
Its revision belongs to that item. Selecting an agent with the wrong role fails.

Assignment results remain full `work.Work` snapshots. Their `work_id`, `assignee`,
`kind`, `revision`, and `state` are authoritative at the mutation boundary.
Success acknowledges ledger registration for asynchronous dispatch, not delivery,
execution, or completion. Do not add a second tool-result envelope for the same facts.

For example, after the worker submits the original task (JavaScript-style tool
orchestration pseudocode; values come from preceding tool results):

```javascript
const auditor = create_agent({role: "auditor"});
let original = get_work({work_id: task.work_id}).work;
assign_audit({
  assignee: auditor.agent_id,
  work_id: original.work_id,
  expected_revision: original.revision,
  submission_id: original.latest_submission_id
});

// After audit_completed reports a failing verdict:
original = get_work({work_id: task.work_id}).work;
const repair = assign_repair({
  assignee: worker.agent_id, // Reuse, or explicitly create another implementor.
  work_id: original.work_id,
  expected_revision: original.revision,
  audit_id: original.latest_audit_id
});

// Subsequent repair progress/submission uses repair.work_id and its current
// revision. A new audit after repair submission addresses original.work_id.
```

## 4. Explicit repair lifecycle

```text
implementation active
  submit_work → needs_check
  assign_audit → checking + active audit child
    submit_audit(pass) → accepted + closed audit child
    submit_audit(fail) → changes_requested + closed audit child
      [owner chooses when and to whom to assign repairs]
      assign_repair → changes_requested + active repair child
      submit_work(repair) → needs_check + closed repair child
      assign_audit → checking + new active audit child
```

Do not introduce a new original-work state solely to distinguish waiting for a
repair assignment from repair execution. An explicit `active_repair_id` makes
that distinction while the original remains `changes_requested`.

### Verdict recording

`Store.SubmitAudit` keeps validation, immutable audit creation, contributor checks,
audit closure, original revision increment, and pass/fail state changes. A pass
continues to complete its scope and release reservations. A fail resets affected
steps to pending and retains the original scope reservations, as today.

A fail no longer constructs repair work or emits `WorkAssigned`. It emits the
existing actionable `AuditCompleted` event with its audit ID and the original work
snapshot. That event wakes the owner; it does not dispatch repairs.

Add these fields to the original implementation record:

```go
LatestAuditID  AuditID `json:"latest_audit_id,omitempty"`
ActiveRepairID ID      `json:"active_repair_id,omitempty"`
```

`LatestAuditID` changes only when a verdict is recorded. `ActiveRepairID` is set
when repair work is assigned and cleared when it is submitted or cancelled.
Retain the latest verdict when a new submission is awaiting review; callers must
check its submission identity instead of treating it as a verdict on the new result.

### Repair assignment

Introduce a narrow store command:

```go
type AssignRepairRequest struct {
    WorkTarget // Original implementation ID and expected revision.
    Assignee identity.ActorID `json:"assignee"`
    AuditID  AuditID          `json:"audit_id"`
}

func (s *Store) AssignRepair(actor identity.ActorID, r AssignRepairRequest) (Work, error)
```

In one existing store mutation transaction:

1. Validate ownership and revision of the original implementation.
2. Require `changes_requested`, no active repair, and an existing failing audit.
3. Require `audit_id == original.latest_audit_id` and that audit's submission ID
   equals `original.latest_submission_id`. Resolve the audit's work record and
   verify its parent is this original implementation; `Audit.WorkID` currently
   identifies an audit child, not the original implementation.
4. Reject a second repair for the same audit. Cancellation ends the implementation
   cycle, so replacement of an active repair uses `reassign_work`, not another
   `assign_repair`. Guard this under the store lock, including concurrent requests.
5. Derive the repair's scope from the union of finding step IDs, ordered according
   to the original scope. For unscoped work, retain a nil scope. Preserve all
   findings in the linked immutable audit; callers cannot select only convenient
   findings. Derive repair input context as specified below.
6. Create an active repair child with `ParentID=original.ID`,
   `RequestedBy=actor`, `RequestedByAuditID=audit.ID`, and the explicit assignee.
7. Set the original's `ActiveRepairID`, increment its revision, and record both
   changed work values through the existing `Change` mechanism. Emit `WorkAssigned`
   for the repair so the existing dispatcher delivers it.

The workflow validates role/lifecycle eligibility before entering the store; the
store owns the authoritative work transition. No runtime calls occur inside the
store transaction. Role eligibility is independent of artifact isolation: work
revisions still cannot prevent shell writes from an already running operation.

### Submission, cancellation, and access

Repair submission must verify `original.ActiveRepairID == repair.ID`, alongside
the existing state/revision checks. It closes the repair, clears the active pointer,
creates a superseding full-outcome submission, and moves the original to `needs_check`.
The original implementation assignee remains historical; the repair's assignee and
the new submission's `SubmittedBy` identify the actual contributor.

Preserve contributor-chain exclusion when assigning or reassigning auditors. A
replacement repair implementor must not later audit its own contribution.

Keep the existing cancellation policy: cancelling implementation or repair ends
that implementation cycle, cancels live children, clears the active repair pointer,
and releases reservations. Cancelling an audit returns its unchanged submission
to `needs_check`. A stopped repair worker leaves work active for explicit recovery.

An audit must remain immutable after recording. Stop populating `Audit.RepairWorkID`
for new verdicts; retain the field only to decode historical records. The forward
link lives in `Work.RequestedByAuditID`, and the original work holds the current
repair pointer. Inspection can derive repair history from those work records.

Update `Store.GetAudit`: its current fallback reads `a.RepairWorkID`. Authorize a
repair worker through its assigned repair's `RequestedByAuditID` instead. Only the
current assignee of a non-cancelled repair gains that access; reassignment revokes
the old assignee's repair-derived access. Preserve any independent existing right
that actor has as owner, submitter, or authorized auditor. Mirror these rules in
passive reads, which already reuse store visibility logic. Do not grant a replacement
repair worker unrelated plan steps or unrestricted access to all submissions.

### Inputs available to a fresh repair agent

Repair assignment must work for an agent with an empty task history. Findings alone
do not identify the submitted artifacts or retain the original task context. The
current repair envelope drops the original context, and source-submission reads
are forbidden to a replacement repair worker; both paths need explicit changes.

At assignment, set the repair's task from the original task, copy the original
task-level `Context` into its `Context`, and derive `ExpectedOutput` from the original
expected output plus the requirement to address every finding and submit again.
Task/context/expected-output values are snapshotted with the repair; the caller
cannot replace them. The original context is task-level shared input under this
contract. Plan step visibility remains limited to the derived repair scope.
Findings stay structured in the immutable audit instead of replacing task context
with a JSON string containing findings.

Extend `get_work(repair_id)` in both workflow inspection and passive inspection to
return the existing `work.Inspection` shape with:

- `work`: the repair assignment and its task context;
- `steps`: only the repair's authorized plan steps;
- `audit`: the full failing verdict and findings referenced by `RequestedByAuditID`;
- `submission`: the exact source submission referenced by that audit, with its
  task, summary, evidence, artifact references, and step snapshots filtered to the
  repair scope. For unscoped work there are no plan steps to expose.

Resolve the source submission through `repair.RequestedByAuditID → audit.SubmissionID`.
Do not use the original work's latest submission, which may have advanced. Do not
add a redundant mutable source-submission pointer to the repair. On closed repair
inspection, this field still describes the input that was repaired, not the later
submission produced by the repair.

Extend `Store.canReadSubmission` and `submissionView` together. The current
assignee of a non-cancelled repair can read precisely its audit's source submission.
Filter step snapshots using the authorized repair scope, not the source submission's
`SubmittedVia` scope, which can include the whole original implementation. Preserve
broader independent rights an actor already has as owner, original assignee,
submitter, or authorized auditor. A repair assignment grants no read access to the
parent work record, unrelated submissions, or unrelated plan steps.

Submission summary, evidence, and artifact references are currently submission-level
values without per-step labels. Share those values for this one source submission;
do not claim per-step filtering of their free text or artifact contents. Assignment
does not open artifacts automatically or grant filesystem isolation. The agent uses
its existing tools to inspect the referenced outcome.

Reassignment and cancellation revoke repair-derived source-submission and audit
access under the same rule. Historical records remain immutable. Inspection of a
fixed historical prefix reflects that prefix's assignments; current model reads
always check current assignments. Give the new implementor this explicit instruction:

> Before repairing, call get_work with your repair work_id. It includes the failing
> audit, the source submission and artifact references, task context, and your
> authorized steps. Read those inputs even if you did not perform the original work.
> Submit against your repair work_id and its current revision.

## 5. Code structure and shared entry points

Introduce a small public `roster` contract package for role and registration values.
This avoids importing the runtime `agent` package into `tool`, importing `harness`
into its workflow dependency, or continuing to use `work.Kind` as an agent role.
It contains data types, not a second runtime, scheduler, or persistence service.

```go
// Proposed roster/types.go
type Role string

const (
    Root        Role = "root" // Bootstrap/inspection only.
    Implementor Role = "implementor"
    Auditor     Role = "auditor"
)

type CreateRequest struct {
    Role Role `json:"role"`
}

type Registration struct {
    AgentID identity.ActorID `json:"agent_id"`
    Parent  identity.ActorID `json:"parent"`
    Role    Role             `json:"role"`
}

// Proposed harness and internal/workflow public operation signatures.
func (s *Session) CreateAgent(ctx context.Context, actor identity.ActorID,
    r roster.CreateRequest) (roster.Registration, error)

func (s *Session) AssignWork(ctx context.Context, actor identity.ActorID,
    r work.AssignmentRequest) (work.Work, error)
```

The workflow keeps the role-to-spec mapping and replaces its private kind-valued
role map with registrations. Its new `CreateAgent` operation:

1. Enters the existing admission gate; checks cancellation and root authority.
2. Validates the role and resolves the already configured implementor/auditor spec.
3. Calls `conversation.Controller.CreateAgent(actor, spec)` to start an idle runtime.
4. Publishes the required registration fact and commits assignment eligibility
   before returning success. Eligibility readers must not see a successful
   registration before its required record is accepted.
5. On registration failure after runtime creation, requests stop, reports cleanup
   errors, and follows existing capture-failure handling. It never returns a usable
   successful registration for an incompletely registered runtime.

Bootstrap registers the root for inspection through an internal path. Root
creation remains part of session construction, not a model-callable role choice.

The controller remains a policy-free primitive: it accepts an explicit `agent.Spec`,
starts an idle loop, and routes messages. Its low-level method is not a second
tracked-creation API. Remove the raw `harness.Session.CreateAgent(parent, spec)`
overload during migration; low-level custom hosts can compose the controller
directly. Do not keep a raw path under the same harness method or HTTP route.

Replace `tool.CreateAgent(Handler[work.Work])` with
`tool.CreateAgent(Handler[roster.CreateRequest])`. Migrate low-level transport tests
to a test-local callback tool with a distinct name such as `create_test_agent`.
Do not install a task-bearing compatibility alias in the default toolset.

`AssignWork` requires and checks the supplied assignee, then switches on kind to
call the existing `Store.AssignWork`, existing `Store.AssignAudit`, or new
`Store.AssignRepair`. `ReassignWork` performs the corresponding role check and calls
`Store.Reassign`. Delete implicit provisioning and its assignment-failure cleanup.

The shared request remains a tagged union with narrow tool-schema branches:

```go
// Proposed replacement in work/commands.go.
type AssignmentRequest struct {
    Kind             Kind             `json:"kind"`
    Assignee         identity.ActorID `json:"assignee"` // Required for every kind.
    Task             string           `json:"task,omitempty"`
    Context          string           `json:"context,omitempty"`
    ExpectedOutput   string           `json:"expected_output,omitempty"`
    Scope            *Scope           `json:"scope,omitempty"`
    WorkID           ID               `json:"work_id,omitempty"`
    ExpectedRevision Revision         `json:"expected_revision,omitempty"`
    SubmissionID     SubmissionID     `json:"submission_id,omitempty"`
    AuditID          AuditID          `json:"audit_id,omitempty"` // New repair selector.
}
```

Within the admitted, validated workflow operation, the dispatch becomes:

```go
// Sketch: validate all branch selectors and reject forbidden fields before this
// switch; role/lifecycle eligibility has already been checked. No provision call.
switch r.Kind {
case work.Implementation:
    return s.Store.AssignWork(actor, work.AssignRequest{
        Assignee: r.Assignee, Task: r.Task, Context: r.Context,
        ExpectedOutput: r.ExpectedOutput, Scope: r.Scope,
    })
case work.AuditWork:
    return s.Store.AssignAudit(actor, work.AssignAuditRequest{
        WorkTarget: work.WorkTarget{ID: r.WorkID, ExpectedRevision: r.ExpectedRevision},
        SubmissionID: r.SubmissionID, Auditor: r.Assignee,
    })
case work.Repair:
    return s.Store.AssignRepair(actor, work.AssignRepairRequest{
        WorkTarget: work.WorkTarget{ID: r.WorkID, ExpectedRevision: r.ExpectedRevision},
        AuditID: r.AuditID, Assignee: r.Assignee,
    })
default:
    return work.Work{}, fmt.Errorf("%w: unknown work kind", work.ErrInvalid)
}
```

Wire decoding is performed by the shared strict decoders before this switch.
All three entry points also encounter the same semantic branch validation here,
including rejecting nonzero `audit_id` for implementation/audit commands and
nonzero `submission_id` for repair commands. The Go struct above is a normalized
command, not the HTTP decoding contract.

Use explicit role-to-kind compatibility, rather than casting strings:

```go
func accepts(role roster.Role, kind work.Kind) bool {
    return role == roster.Implementor &&
        (kind == work.Implementation || kind == work.Repair) ||
        role == roster.Auditor && kind == work.AuditWork
}
```

Application validation rejects unregistered, stopped, stop-requested, or incompatible
agents. Preserve support for paused agents: accepted work waits until an explicit
resume. Do not infer eligibility from tool names, prompts, parentage, or `idle`.
Do not introduce a one-work-item-per-agent limit in this change; existing agents
can receive multiple assignments and execute sequentially. A stop racing with
assignment is handled by the existing delivery/exit recovery path, not a promise
that validation guarantees eventual execution.

## 6. Go, HTTP, inspection, and event parity

The model tool calls the workflow method with `tool.Call.Actor`; the Go method
delegates to that same operation; the authorized HTTP handler calls the Go method.

Proposed `POST /sessions/{id}/agents` body:

```json
{"actor":"agent-1","request":{"role":"implementor"}}
```

This matches the existing work-command envelope and returns the registration with
HTTP 201. Remove the raw `{parent, profile}` route and `Options.AgentProfile` hook
from this tracked surface. Host provider/prompt/tool customization stays in the
existing session role configuration and dependencies. HTTP authorization still
confers trusted host access; the supplied actor ID is not authentication.

Add an application registration event, for example
`conversation.AgentRegistered{Registration roster.Registration}`, following the
existing application `WorkEvent` pattern. The controller does not create this event
or interpret the role. Encode it as `agent_registered` and reduce it in the harness
projector. Keep the small registration fields inline when records are framed.

Do not add application role policy to `agent.Spec` or `conversation.AgentInfo`.
Instead, add an application agent view that combines runtime state, recorded
registration, and projected assignments. Use the same view for model list/inspect
tools, Go/HTTP reads, archive inspection, and the TUI's role display:

```json
{
  "agent_id":"agent-2",
  "parent":"agent-1",
  "role":"implementor",
  "registered":true,
  "state":"idle",
  "eligible_work_kinds":["implementation","repair"],
  "active_work_ids":["work-1"]
}
```

Eligibility reflects role, registration, and observed lifecycle at that snapshot;
assignment validates again. A terminal or stop-requested agent has no eligible
kinds. `active_work_ids` refers to work in `active`, not submitted outcomes waiting
for audit. Use `list_work` for discovery across all lifecycle states. Define shared
agent view types below `harness` (in `harness/projection`, with public harness aliases)
so archive inspection can use them without importing its parent harness package.

A concurrent reader may observe `agent_started` before `agent_registered`; show it
as unregistered and ineligible during that interval. Historical traces without a
registration fact show role unknown, not an inferred role or an invented registration.
Replay reconstructs observations; it does not restart agents or resume execution.

Use a new record schema version for the registration event and revised lifecycle
contract; retain schema 2/3 decoding. Update validators and frame reduction explicitly.
Historical automatic-repair events and `repair_work_id` remain readable as recorded.
Never replay an old failing audit by executing the new repair-assignment operation.

### Work discovery and pagination

Add a read-only `list_work` tool to the root, backed by
`Session.ListWork(ctx, actor, work.ListQuery)` and an authorized
`GET /sessions/{id}/work?actor=...` endpoint. Its initial scope is root-owned work;
nonroot callers receive forbidden. It does not introduce broader worker visibility.
The same filtering, summaries, and fixed-prefix pagination are available through
archive inspection. Keep the existing `GET /work/{work_id}` detail endpoint.

First-page tool arguments are optional `assignee`, `kind`, `state`, and `limit`.
Omitting state includes every lifecycle state; do not default to active. Match
assignee against the current binding at the selected snapshot. An omitted assignee
lists all root-owned work, which also supports recovery when an assignment has been
reassigned since its creation. Unknown enum values and empty supplied identifiers
are invalid. An unmatched valid filter returns an empty page.

```javascript
const page = list_work({assignee: worker.agent_id, limit: 20});
// Includes matching implementation, audit, and repair items in every state.
const next = list_work({cursor: page.next_cursor, limit: 20});
// Call only when next_cursor is nonempty; filters are retained by the cursor.
```

Proposed public read types in `work/commands.go`:

```go
type ListQuery struct {
    Assignee identity.ActorID `json:"assignee,omitempty"`
    Kind     Kind             `json:"kind,omitempty"`
    State    State            `json:"state,omitempty"`
    Cursor   string           `json:"cursor,omitempty"`
    Limit    int              `json:"limit,omitempty"`
}

type Summary struct {
    ID                 ID               `json:"work_id"`
    Kind               Kind             `json:"kind"`
    State              State            `json:"state"`
    Revision           Revision         `json:"revision"`
    Owner              identity.ActorID `json:"owner"`
    Assignee           identity.ActorID `json:"assignee"`
    ParentID           ID               `json:"parent_id,omitempty"`
    RequestedByAuditID  AuditID          `json:"requested_by_audit_id,omitempty"`
    LatestSubmissionID SubmissionID     `json:"latest_submission_id,omitempty"`
    TaskPreview        string           `json:"task_preview"`
}

type ListPage struct {
    Items      []Summary `json:"items"`
    NextCursor string    `json:"next_cursor,omitempty"`
}
```

Default page size is 20, maximum 100. Omitted limit uses the default; supplied
limits must be 1–100 on the wire. Direct Go's zero limit means the default. Summaries
contain no evidence or artifact bodies; truncate task previews to 240 Unicode code
points. Read full current details with `get_work` after selecting an item.

The first page captures a finite accepted-log prefix. Sort items by first recorded
appearance, newest first, breaking ties by work ID. Continuation cursors encode
the session, selected prefix, filters, and last ordering key; callers treat them
as opaque. Subsequent pages accept only cursor and optional limit, not replacement
filters. Validate session identity, ordering key, prefix availability, and root
authority. A cursor conveys no authority and must not permit future-prefix reads.

Evaluate both membership and work state at that same prefix across all pages, so
concurrent submission or reassignment does not cause missing or duplicated items.
Start a new query to see later changes. Return an explicit unavailable-prefix error
if retention or disposal prevents completing that snapshot; do not silently switch
to current state. Empty pages use `items: []` and omit `next_cursor`.

The harness inspection layer owns record cursors and pagination. The work read model
supplies authorized work snapshots and summaries without importing eventlog. Build
the list from accepted work changes using existing fixed-prefix inspection machinery,
retaining each work ID's first-appearance key. Resolve full framed work values when
needed for owner/assignee filtering and previews; the existing compact `WorkHeader`
contains too few fields to perform those filters alone. Historical records use the
same resolver rather than treating missing header fields as empty domain values.

After a lost assignment response, query all states, inspect candidate work items,
and reconcile their task, assignee, and parent/audit linkage before deciding another
action. A filtered query with no match may require an unfiltered query after
reassignment. A response may be lost before the mutation commits: one empty snapshot
is not proof that the operation failed. If several items match, report the ambiguity
instead of automatically creating another item. This supplies discoverability,
not operation correlation, deduplication, or exactly-once execution.

## 7. Errors, ownership, and recovery

| Case | Result and recovery |
|---|---|
| Unknown/missing role or assignee | Invalid request; no creation or assignment mutation |
| Nonroot creation/assignment | Forbidden; workers request help from the owner |
| Agent exists but lacks registration | Explicit ineligibility error; caller selects a registered agent |
| Role incompatible with work kind | Error states required role and actual role |
| Creation succeeds; assignment validation fails | Agent remains idle/available; correct assignment, reuse, or stop explicitly |
| Registration fails after runtime startup | Creation fails; runtime is stopped as cleanup; capture failure remains visible |
| Stale work revision or stale audit | Conflict; read current original work before deciding a new action |
| Repair already assigned | Conflict identifies active repair; inspect or reassign it |
| Work accepted but delivery fails | Keep tracked work visible; notify owner; explicit reassignment/cancellation |
| Create response is lost | Inspect registered agents before retrying; creation is not deduplicated |
| Assignment response is lost | Use list_work across all states, then get_work on candidates; query without assignee after possible reassignment; do not blindly retry an empty or ambiguous result |

Continue using existing typed error categories and HTTP mappings, adding concrete
messages rather than returning plain errors that become HTTP 500. Revise tests for
unknown role, registration, role mismatch, and terminal-state distinctions.

Changing assignee does not stop the old runtime. Cancelling work does not stop its
agent. Stopping an agent does not implicitly cancel or reassign its work. These
separate lifecycles retain the existing revision fencing and owner recovery behavior.

## 8. File-level implementation map

Paths below are relative to the repository root; new files are explicitly marked.

| Files | Proposed changes |
|---|---|
| `roster/types.go` (new) | Role, creation request, registration value types |
| `tool/agent.go`, `tool/agent_test.go` | Replace task-bearing creation callback; role enum, description, validation |
| `tool/work.go`, `tool/work_test.go` | Require assignees; add repair schema branch; remove implicit-provisioning descriptions |
| `tool/work_contract.go` (new) | Shared compiled branch contracts, pure assignment/reassignment decoders, normalization; creation decoder shares its creation contract |
| `tool/work_list.go` (new) | Root list_work schema, bounded page arguments, callback adapter |
| `work/commands.go` | Normalized assignment command and semantic validation; repair selector; list query/page/summary types |
| `work/types.go` | Required assignee JSON tags; `AssignRepairRequest`; original latest-audit/active-repair pointers; legacy audit link annotation |
| `internal/workflow/session.go` | Registration map, root creation tool, changed role prompt/tool assembly and dispatcher expectations |
| `internal/workflow/operations.go` | Explicit creation and registration; eligibility; three assignment branches; explicit reassignment |
| `work/review.go` | Separate failing verdict from repair assignment; repair submission/pointer and cancellation rules |
| `work/store.go`, `work/readmodel.go` | Repair-derived audit/source-submission access and scope filtering; fresh-worker inputs; authorized work enumeration and summaries |
| `harness/session.go`, `harness/agents.go`, `harness/work.go` | Replace raw public creation; root registration; shared operations and enriched agent views |
| `harness/prompts.go` | Role selection, two-call delegation, explicit audit/repair/replacement, worker escalation |
| `conversation/events.go` | Application registration event; retain policy-free controller creation |
| `harness/eventcodec/events.go`, `harness/eventcodec/publish.go`, `eventlog/` | Registration encoding/decoding/framing and record-version compatibility |
| `harness/projection/projector.go`, `harness/record/work.go`, `harness/work_projection.go` | Registration and agent view types, new work fields, derived repair links, work first-appearance index, framed-value resolution |
| `harness/inspection/`, `harness/transcript.go` | Consistent agent views, scoped repair inputs, fixed-prefix work listing and cursors across live/archive inspection |
| `harness/list_work.go` (new) | Shared root-authorized ListWork operation delegating to the read-only inspection path |
| `harness/httpapi/handler.go`, `harness/httpapi/service.go` | Role creation endpoint; raw command envelopes and shared pure decoders; work listing endpoint; shared validation and errors |
| `internal/tui/tui.go`, `internal/tui/agents.go` | Adapt harness agent view; display role and explicit waiting-for-repair state |
| `examples/delegation/main.go`, `examples/local/main.go` | Explicit creation, audit assignment, repair assignment, and replacement |
| `conversation/*_test.go`, `provider/chatcompletions/client_test.go` | Distinguish low-level test callback from production creation contract |
| `README.md`, `DESIGN.md`, `WORK_DESIGN.md`, `HARNESS_DESIGN.md`, `harness/httpapi/README.md`, `harness/RECOVERY.md` | Update public contract, examples, lifecycle, migration, and historical record semantics |

## 9. Verification and implementation order

Implement as two coherent changes; both are part of this proposal.

**A. Explicit registered creation.** Land role contracts, creation operation,
registration records/views, required assignment/reassignment assignees, tools,
strict wire decoders, all-state work listing, prompts, host/HTTP parity, examples,
and migration together. During this intermediate
version, document that failed audits still automatically assign repairs. Do not claim
the final explicit repair contract until B lands.

**B. Explicit repair assignment.** Land store transitions and links, schema branch,
fresh-worker context and scoped source-submission access, prompt/event changes,
projections, and the full audit/repair examples
together. Remove automatic repair dispatch rather than leaving two ways to start it.

Required deterministic checks:

- Creation advertises the role enum, starts idle without invoking the provider,
  registers the correct tools/role, rejects nonroot callers, and handles cancellation
  and required-record failure without leaving a usable partial registration.
- Assignment and reassignment never increase the agent count; omitted assignee
  fails in tool, direct Go, and HTTP paths. Failed assignment leaves the chosen agent
  alive. Reuse, paused delivery, stopped agents, wrong roles, and stop races are covered.
- A shared JSON fixture matrix exercises creation, assignment, and reassignment
  through both tool and HTTP decoders: valid branches, null, forbidden fields with
  empty values, wrong types, nested unknown fields, and missing required arguments.
  Rejected inputs invoke no callback or mutation. Valid inputs normalize identically;
  direct Go commands receive the same semantic checks without claims about field presence.
- A failing verdict creates exactly one immutable audit and zero repair work items
  or assignment events. Owner notification still arrives without a UI reader.
- A repair assignment derives all findings, records both changed work snapshots,
  accepts a new eligible implementor, and rejects wrong owners, passing/stale audits,
  wrong submissions, widened scopes, and concurrent/duplicate repairs.
- Repaired submission closes the active repair, preserves the supersession chain,
  and requests a new audit. All contributors remain excluded from reviewing it.
- Cancellation before repair assignment, cancellation during repair, reassignment,
  worker exit, and failed delivery preserve reservations and explicit recovery rules.
- Audit visibility works for a replacement repair worker and is revoked on reassignment
  or cancellation when no independent permission exists. Immutable audit bytes do not
  change when repair work is assigned.
- A newly created repair implementor with no prior task history gets the source
  submission, artifact references, original task context, complete findings, and only
  authorized step snapshots through get_work. It cannot read unrelated submissions
  or parent work. Reassignment/cancellation revoke source-read rights; independent
  owner/submitter rights and post-submission repair inspection remain consistent.
- Lose an assignment response, let the worker submit and receive acceptance, and
  recover the work through list_work despite its absence from active_work_ids.
  Exercise reassignment, cancellation, multiple plausible matches, and an in-flight
  mutation missing from the first snapshot; no test treats listing as deduplication.
- Paginate work while new work is assigned and existing work changes state. Fixed
  prefix pages contain no omissions/duplicates, retain filters, reject cross-session
  or unavailable cursors, enforce root authority, and respect count/preview bounds.
  Verify the same results through tools, Go, HTTP, and live/archive inspection,
  including historical records with framed work bodies.
- Scripted full-cycle tests exercise implementation → fail → explicit repair → pass
  through model tools, Go operations, and HTTP. Compare emitted records and passive
  views as well as final state.
- Live and archive inspection agree on role, eligibility, work links, and registration
  boundaries, including framed records. Existing schema 2/3 fixtures still decode.

Run `go test -race ./...` after implementation. Existing passing tests are a baseline,
not proof of these proposed semantics. Replace tests that assume automatic provisioning
or repair rather than preserving obsolete behavior behind an alias.

Separately evaluate model discovery with the configured providers using: “create an
agent and do X, Y, Z,” “reuse that agent,” “have another agent audit this,” “repair
the failed audit,” and “replace the stopped worker.” Grade actual tool sequences,
role choices, assignees, and outcomes. Include a worker requesting help from its owner.
Schema-string assertions and scripted providers cannot establish natural-language
discovery. Live-model results from implementation are recorded at the beginning of this document.

The design intentionally leaves custom model-selected roles, nested delegation,
idempotency keys, agent pools, and execution resumption out of these two changes.
Those can extend the explicit contracts later without reintroducing hidden creation.
