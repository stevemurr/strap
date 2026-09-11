# The minimal conversation core

This implements the narrowed design: communication and model interaction first.
One controller owns one conversation. The application establishes its persistent
root and configures the tools that can create other agents. Agents own their
sequential execution loops.

The implemented work ledger, shared plans, and implementation/audit/repair flow
are described in [WORK_DESIGN.md](WORK_DESIGN.md). Application wiring composes this
with the conversation core; the controller does not own work policy.

## Complete map

```text
Application setup
    ├── conversation.New(ctx)
    ├── agent.Spec { provider, prompt, tools }
    └── CreateAgent(user, rootSpec) → establish root
    │
Host / user
    │
    ├── Send(root, text) → queued receipt
    └── NextEvent → messages, acknowledgments, creation, exit
    │
Conversation controller
    ├── Root identity
    ├── Registry of owned agents
    ├── Message routing and receipt state
    ├── Host event inbox
    └── Agent goroutines and cancellation
            │
            └── Agent (same type for root and children)
                ├── prompt.Prompt → system message
                ├── Local model history with ordered text/image content
                ├── Inbox[message.Message]
                ├── Bound message.Sender
                ├── provider.Provider
                └── tool.Tool instances
                    ├── assign_work → application callback → work store + controller (if supplied)
                    ├── send_message → bound sender (if supplied)
                    ├── message_status → receipt lookup (if supplied)
                    └── Supplied tools
```

No agent can mutate another agent's inbox through the public agent API. An
outgoing draft carries no sender identity; the controller-bound `message.Sender`
fills it in. The controller does not hold its lock while calling a model or tool.

## Interfaces and types

| Package | Public boundary | Meaning |
|---|---|---|
| `conversation` | `New(ctx)` | Construct an empty conversation |
| `conversation` | `CreateAgent(parent, spec)` | Start an idle agent; first user-parented creation establishes root; return `Creation{AgentID}` |
| `conversation` | `Send(to, text)` | Route user input and return a queued `message.Receipt` |
| `conversation` | `Receipt(id)`, `Agents()`, `InspectAgent(id)` | Return delivery and lifecycle snapshots |
| `conversation` | `NextEvent(ctx)` | Single-consumer host event stream |
| `conversation` | `PauseAgent(id)`, `ResumeAgent(id)`, `StopAgent(id)` | Return a lifecycle acknowledgment as `(AgentInfo, error)` |
| `conversation` | `Close(ctx)` | Cancel and join the conversation |
| `agent` | `New(Config)`, `Run(ctx)` | Assemble and drive a single persistent loop |
| `inbox` | `Inbox[T]`: `Send`, `Receive`, `Drain`, `Close` | In-memory FIFO with a notification channel |
| `message` | `Message`, `Draft`, `Receipt`, `Sender` | Structured work, addressing, correlation, and delivery milestones |
| `work` | `Store`, `Plan`, `Work`, `Submission`, `Audit` | Authoritative shared work and review outcomes |
| `identity` | `ActorID` | Transport-independent actor identity |
| `prompt` | `Prompt.Render`, `Prompt.Clone` | Serialize operating instructions and snapshot configuration |
| `provider` | `Provider.Submit(ctx, Request) (Response, error)` | Provider interaction, including text and tool calls |
| `tool` | `Tool.Definition`, `Tool.Call(ctx, Call) (Result, error)` | Model-visible operation and ordered text/image results |
| `content` | `Content`, `Part`, `Image`, `Clone` | Provider-independent text/image payloads and snapshots |
| `tool` | `Call{Arguments, Actor, Sender}` | Model arguments plus runtime-supplied caller identity and routing |
| `tool` | `AssignWork(handle)`, `UpdatePlan(edit, progress)`, `SubmitWork(handle)`, `SubmitAudit(handle)` | Tool contracts with operations supplied by the application |

The controller, agent, and inbox are concrete types. Interfaces exist for injected
provider, tool, and outgoing-message implementations; there is no umbrella runtime
interface or separate worker type.

## Terminal host

`cmd/strap` configures the HTTP provider, creates one conversation, runs the UI,
and cancels and joins agents on exit. `internal/tui` owns only terminal state: the
input draft, input history, displayed transcript, scroll position, and activity
indicators derived from events.

Its `Session` interface exposes `Root`, `Send`, `Agents`, `NextEvent`, and
inspection, pause, resume, and stop operations. Terminal commands request those
operations without implementing lifecycle behavior in the UI.
The application workflow session consumes controller events and relays them to
the terminal while dispatching work independently. One asynchronous relay read
feeds the Bubble Tea update loop while keyboard input remains available. Enter queues a user message through the controller; model calls
and tool execution remain inside agent goroutines. Quitting cancels the event read
and returns to the CLI for conversation shutdown.

The UI neither owns model history nor implements a second agent loop. Clearing the
screen does not reset the conversation. Responses are displayed in full because
the current provider contract is non-streaming.

The developer transcript displays routed messages, delegation, and a vertical
timeline of readable tool names attributed to their calling agents. An uninterrupted
run of tool entries renders as one line, grouped by actor and tool name with counts.
Grouping happens during rendering, so individual events and frozen snapshots stay
intact. It omits raw tool arguments, call IDs, and results.
Delivery receipts update pending-message bookkeeping without adding transcript
entries. `Idle` is the resting agent status; `queued` describes message delivery. `agent.Config.OnTool` reports `ToolActivity` before and after
actual dispatch; the controller wraps it in a `ToolEvent` with the actor ID.
`FinishedAt` is zero for a start notification. The finish notification includes
the result or error, including cancellation and unknown-tool errors. Each event
owns its argument and result bytes. These host events never enter agent inboxes
or add messages to model history. Observers enqueue notifications without waiting
for a UI consumer.

The UI derives a spinner and elapsed timer from pending input, agent state, and
active tool calls. Delegated work stays visible when the root is idle. It retains
the duration of the last active period when idle; active tools show individual
durations. All remote text, including tool metadata, is stripped of terminal
control sequences. Raw image results remain in the core event stream only.

Terminal mouse reporting is disabled so native text selection and copying work.
F2 caches the displayed view while events continue to arrive; keyboard scrolling
and resize operate on the frozen transcript. F2 again displays collected entries.
This is a display operation, not an agent pause or a second event consumer.

Conversation bodies use Glamour with a dedicated theme in `markdown_style.go`.
Heading levels use weight, color, and small markers instead of raw hash prefixes.
Light and dark palettes include code frames, inline code, lists, tasks, quotes,
links, tables, rules, and definitions. Named syntax themes avoid Glamour's shared
custom-theme registry, so rendering a light theme cannot change a dark theme.
Color-disabled output retains structural markers and omits syntax colors. Each entry caches its
rendered body by viewport width; new events and spinner ticks do not reparse old
messages. Resizing invalidates those caches. Diagnostics remain literal text.
Input text is sanitized before parsing; rendered output retains SGR text styling
while removing other terminal controls, including those introduced by Markdown
entity decoding. Tables and code lines are fitted to the viewport width.

## Provider boundary

`provider.Provider` submits requests to a configured model server. The name
describes the transport/integration responsibility; it does not imply the adapter
performs model computation or requires a reasoning feature.

```text
Agent → provider.Provider.Submit → provider/vllm or provider/chatcompletions
                                → provider/internal/chatwire → local HTTP server
      ← provider.Response       ← decoded text / tool calls
```

The CLI selects the concrete backend and resolves an application-owned generation
preset followed by explicit flag overrides. Model aliases and agent roles never
select presets inside an adapter. `provider.Request`, the agent loop, and the
workflow/controller carry no sampling or vLLM fields.

`provider/vllm` owns its complete, typed generation configuration and the private
JSON fields for vLLM extensions. Its constructor validates supplied values and
copies every pointer into an immutable snapshot. Nil omits a field; zero and false
are sent explicitly. Clients retain no conversation or reasoning history and can
be shared across agents. The injected HTTP client remains a shared collaborator.
`provider/chatcompletions` keeps its existing configuration and server defaults.
Both adapters use internal `chatwire` helpers for transport and message conversion;
neither imports the other, and the internal helpers contain no presets or vLLM
option policy. No second backend interface or runtime capability discovery is needed.

The adapter owns wire translation and HTTP errors. It sends model history and tool
definitions, preserves tool-call IDs through subsequent results, and translates
JSON-string function arguments into the core's `json.RawMessage`. It translates
image parts into data URLs, keeping tool-call IDs/results contiguous and inserting
labeled image messages after the complete batch. This accommodates the protocol's
text-only tool messages while preserving images in internal tool-result history. Actor and envelope
metadata are not added as unsupported HTTP fields; attributed content remains in
the model messages. Tool execution stays in the agent loop.

The adapter uses complete responses, honors cancellation, and rejects malformed or
truncated completions instead of dispatching incomplete tool calls. There is no
provider registry, discovery loop, automatic retry, or change to agent lifecycle.

`provider.Response.Usage` carries optional, nonnegative `int64` input and output
counts for one call. Shared `chatwire` decoding maps `prompt_tokens` and
`completion_tokens`; omitted, null, malformed, or negative counts are unavailable,
independently per field. Reported zero is preserved. Optional accounting cannot
invalidate otherwise usable output. There is no separate provider usage interface
or mutable accounting on shared provider clients.

`Submit` may return valid usage alongside an error for rejected output. Adapters
preserve that usage while discarding invalid text/tool calls. The agent records
usage immediately after each `Submit` returns, before checking its error or
cancellation. A transport failure without reported usage counts as a missing report.

Each agent owns synchronized totals and the latest `UsageObservation`, identified
by its per-agent call number and submitted history revision. `Agent.Usage()` and
`InspectAgent` return independent snapshots, including after exit. Missing-report
counts indicate whether each accumulated total is incomplete. Calls are counted
when they return; an in-flight call is not yet included. `OnUsage` publishes an
independent observation through the controller's host-only `UsageEvent`, relayed
by the workflow session. Observers cannot mutate accounting; events never enter
model history or agent inboxes. No unbounded usage ledger is retained.

Cumulative input usage includes repeated history across calls. The latest input
measurement describes only the submitted revision, before any subsequent response,
tool result, or inbox append. Future compaction must count or estimate the next
assembled request; these observations do not supply that capability. Context
limits, token estimation, and compaction policy remain downstream.

`provider.TokenCounter` is an optional, separate capability:
`CountTokens(context.Context, Request) (int64, error)`. The vLLM adapter implements
it with an explicit `/tokenize` request, sharing message/tool translation and HTTP
transport with generation. It sends the same model and chat template settings,
including the generation prompt, but no sampling options or output budget. Counts
come from the server, must be nonnegative, and describe only the supplied request.
The adapter neither estimates missing counts nor retains mutable accounting.
Counting failures preserve HTTP diagnostics and cancellation. Generic Chat
Completions providers need not implement this capability. The agent loop does not
automatically count requests or alter its usage totals; compaction and context
budget decisions remain caller policy.

After appending every result in a tool batch, `OnToolBatch` publishes its call IDs
and history revision through `conversation.ToolBatchEvent`. The TUI invokes
`CountAgentTokens` asynchronously for that boundary, using a ten-second timeout.
The controller selects the owning agent; its append-only thread supplies an
independent snapshot through the recorded revision, together with its original
tool definitions. Provider I/O holds neither controller nor history locks, and
headless consumers do not trigger counting automatically. Counts never enter
model history or usage accounting. Tool rows display the latest completed batch
per agent, preserving earlier row counts, scroll position, and frozen copies;
unavailable measurements stay visibly unavailable.

The scripted example exercises a failed audit, scoped repair, and passing audit
without a server. The local example
uses a configured model at `127.0.0.1:1234`; success follows accepted work rather
than a particular spelling of the final answer.

## Agent loop

```text
Wait for inbox message
    ↓
Append message to model context; publish consumed acknowledgment
    ├── Observation alone → return to inbox without calling model
    ↓
Consume other currently queued messages in FIFO order
    ↓
Provider.Submit(context, tools)
    ↓
Append assistant response
    ├── Tool calls → execute sequentially → append all results → consume inbox → Submit again
    └── Text only  → route reply → wait for inbox

Cancellation / model failure → exit loop → report pending input and agent exit
```

Messages arriving during a model call queue. Input is consumed at the next turn
boundary, after any tool batch has settled. A text-only answer is routed before
returning to the inbox. This core does not discard a response or prevent its tools
from executing because newer input arrived while it was generated.

The model receives copies of history and tool definitions. It returns ordinary
text and tool calls, never a lifecycle proposal. Tool errors are represented as text
tool results; successful results may contain ordered text and images. Model failures exit the agent and are routed to its parent as a
failure message. The model is not called once per acknowledgment.

## Typed tool contracts

The runtime retains `Tool.Definition() provider.ToolDefinition` and
`Tool.Call(context.Context, Call) (Result, error)`, allowing different tool argument
types in one registry. `Func[A]` implements that interface with `Definition[A]`
and `Handler[A]`. No separate binding operation is needed.

`NewParameters[A]` compiles one immutable contract from a struct and declared
constraints. JSON fields without `omitempty` are required. Pointers and slices
preserve omission; explicit null, unknown/duplicate keys, unsupported codecs and
ambiguous fields are rejected. Supported values are structs, pointers, slices,
strings, booleans and integers. Numeric bounds include the Go destination range;
integral JSON decimals/exponents decode exactly without float64 rounding.

Constraints such as enum membership, string length and configured timeout bounds
are declared once and used by both the schema and decoder. Invalid definitions
fail during parameter construction. Agent registration checks typed tools for an
initialized contract and non-nil handler. Built-ins use typed handlers; direct
custom implementations of `Tool` remain responsible for their own contracts.
Definitions and handlers must not be mutated after registration. Parameter
contracts may be shared concurrently, and exported schema bytes are independent.

`Compose` snapshots complete typed tools and advertises their schemas under
`oneOf`. It validates all branches before invoking exactly one handler; no match
or ambiguity returns an argument error with no handler effects. To accommodate
servers that use top-level property types when parsing generated tool arguments,
composition also derives redundant type hints from the branches. These hints do
not replace or loosen the `oneOf` constraints. They prevent arrays and numbers
from being returned as strings by the configured local server. Full alternatives
remain necessary for provider-side validation; runtime validation always applies.

The workflow exposes creation/editing to the root, scoped progress to implementors,
and work-level reporting to auditors. The work store still enforces ownership,
scope, revisions and legal transitions under its lock. Tool contracts do not confer
authority or replace those checks.

## Message and acknowledgment contract

```text
Send → controller assigns message ID and orders delivery
     → message event and queued receipt
     → recipient inbox
     → append to model context
     → consumed receipt
     → eventual model response routed as a separate message
```

- `Message` identifies sender, recipient, kind, content, a structured work snapshot,
  or a work event, and optional `ReplyTo`. Work travels in the `work` field. A copied
  work record never creates store authority.
- `observation` incorporates progress without initiating a model call by itself;
  `notification` wakes the recipient. Neither changes reply correlation. Work and
  submission IDs remain in the event payload.
- `Queued` confirms delivery to an agent's in-memory inbox (or the host event
  stream for a user-directed message).
- `Consumed` confirms incorporation into model context. It does not mean adopted,
  agreed with, executed, or completed. Structured instruction adoption is deferred.
- `Undelivered` identifies queued input left unconsumed when an agent exits.
- Receipts are observable through host events and `Receipt`/`message_status`.
  They do not enter model inboxes and cannot cause acknowledgment ping-pong.
- Text responses go to the creating parent; the root's parent is the user.
  `ReplyTo` identifies the latest consumed message for that exchange. When input
  was batched, that does not assert the response individually resolves every input.
- Replying ends an exchange, not the agent. Children can receive follow-up work.
- Message IDs are unique within a controller instance. This core does not persist
  or deduplicate sends across caller retries or process restarts.

## Delegation and review

The CLI root calls `assign_work` with kind `implementation`, a task, and optional
plan scope. Application wiring creates an idle implementor with a snapshotted
spec, records its work, and dispatches the store-issued snapshot. Invalid work
registration stops a newly provisioned agent. Existing suitable agents can be
selected explicitly. The root alone receives assignment and recovery tools.

Implementors call `update_plan` with a work ID/revision to report progress, then
`submit_work` to capture an immutable outcome. Submission suspends writes and
emits a review request. The root assigns an auditor through the same `assign_work`
tool with kind `audit`, original work ID/revision, and exact submission ID.

The application owns distinct auditor specifications and role membership. Auditors
cannot implement or repair. `submit_audit` records pass/fail atomically; a fail
creates repair work restricted to the findings and directed to the submitted
outcome's implementor. Repair reassignment does not transfer the original work's
broader scope. Submission views for narrow repair actors are filtered, while the
canonical outcome remains complete for the owner and auditor.

Blocked auditors report `Work.Blocker`, remain active, and notify the owner without
producing a verdict. Passing completes the whole submitted scope. Repair submission
returns the original work to `needs_check`. Text replies do not change work state.

The store emits pending events; the application delivers them outside store locks.
Owner events remain pending until consumed. Assignment receipts track recipient and
assignment binding so old delivery failures cannot invalidate new assignments.
`reassign_work` can provision a replacement when its assignee is omitted. Cancellation
informs affected agents, but does not interrupt already-running tools or isolate
shared files. Cancelling implementation/repair ends that implementation cycle;
cancelling only an audit returns its unchanged submission for another review.

`conversation.Deliver` is a trusted host routing entry point. Model messaging uses
the bound `Sender`. The controller knows neither work permissions nor role policy.
Its single event reader belongs to the application workflow session, which forwards
host events to the terminal even when no UI reader is active.

## Prompts and work snapshots

All agents use `agent.Spec{Provider, Prompt, Tools}`. `Spec.Clone` copies prompt and
tool configuration; providers and tool implementations remain shared collaborators.
Nothing is inherited implicitly. The controller adds no tools or operating policy.

Messages carry `*work.Work` instead of `message.Assignment`. Work and event snapshots
are deeply cloned across inbox, provider, and host boundaries. Tools decode narrow
operation requests, not arbitrary mutable work records. `tool.Call.Actor` and the
bound sender come from the runtime; only arguments come from the model.

The low-level `create_agent` callback remains available for custom hosts and core
transport tests. It decodes only task, context, and expected output into a task-only
work value. It is not configured by the CLI and grants no ledger authority. Hosts
using it for tracked assignments must register work before delivering its snapshot.

## Lifecycle ownership

`New(ctx)` creates an empty controller. Its `Root()` is empty until the first
successful `CreateAgent(message.User, spec)` establishes the single root. A failed
creation leaves that slot available. The application can therefore construct
callbacks that capture the controller before assembling the root spec.
Every later `CreateAgent` call must name an existing active agent as its parent;
using the user as a parent cannot create another root, even after the root stops.
The root identity remains stable for the lifetime of the conversation. This is a
creation tree: messages can cross branches, and the controller owns all lifecycles.

The controller registers an agent before starting its goroutine. The agent waits
for its inbox; creation supplies no message and triggers no model call. Sending
work is a separate operation. Acknowledgments and routing are serialized through the controller;
independent agents may call models concurrently. Go scheduling can change the
arrival order between independent producers, but each inbox preserves the order
the controller accepted. There is no claim of deterministic model output or replay.

Stopping an agent requests cancellation. Its exit event follows actual return
from the loop. Stopping a parent alone does not stop its children: all agents are
conversation-owned in this core. `Close` cancels and joins all of them. A timed-out
`Close` retains ownership and may be called again. A provider or tool must cooperate
with its context; signaling cancellation does not kill arbitrary code.

## Verification

`go test -race ./...` exercises FIFO delivery and close races, persistent root
history, asynchronous delegation, sender attribution, steering during a model
call, consumed versus queued acknowledgments, model failure routing, and shutdown.
Tests hold model calls on channels to establish interleavings without sleeps.
Provider tests use temporary HTTP servers to check request encoding, complete tool
round trips, HTTP errors, malformed/truncated responses, and cancellation. The
live example is opt-in and is not called by the test suite.

Terminal tests cover input during model activity, history and draft preservation,
commands, errors, resize wrapping, cancellation of event reads, and terminal escape
filtering, tool traces, timers, delegated activity, and display freezing while events
continue. Controller tests verify tool notification ordering, cancellation, and
snapshot isolation from tool arguments and model history. CLI tests validate flags
without opening a terminal or contacting a model.

Creation tests verify idle startup, explicit prompt overrides, the configured prompt and
application-selected creation tools, structured assignment delivery, snapshot isolation,
invalid arguments, and cancellation between creation and sending.

An HTTP integration test runs the controller and agent loops through the real
Chat Completions adapter. The receiving server checks the exact system prompt,
structured assignment, and absence of `create_agent` from delegated requests.

Boundary tests also cover empty-controller shutdown, failed root initialization,
concurrent root creation, creation during shutdown, absent tool injection, and a
shared creation adapter invoked by both root and nonroot agents. They verify the
configured provider and prompt, caller attribution, and correlated delivery receipts.

The `tool` package imports neither `conversation` nor the core `agent` package.
`CreateAgent` accepts a function with the signature
`func(context.Context, tool.Call, work.Work) (tool.Result, error)`.
`MessageStatus` accepts `func(message.MessageID) (message.Receipt, bool)`.
Tool tests exercise both functions without a controller, checking decoded fields,
runtime metadata, callback outcomes, canceled calls, and invalid inputs.

## Lifecycle transitions

```text
idle ↔ running
  └── pause_requested → paused → running (resume)
              └────────────────→ running (retract pending pause)
any live state → stop_requested → stopped
running → failed (provider/runtime error)
```

The agent owns its pause gate and state. The controller routes control requests,
returns state snapshots, and owns cancellation/joining. `AgentStateChanged` records
ordered transitions. Pause acknowledgment is separate from pause acceptance.

A checkpoint admits each operation. A pause accepted after that checkpoint waits
for the admitted operation to finish. A model response or tool result is recorded
before the next checkpoint. Consequently, a pause can retain a completed model
response, park between tool calls, or defer a text reply without replaying work.
Idle waits observe controls without removing messages from the inbox. Paused input
stays queued; only resume allows further consumption. Cancellation releases idle
and paused waits, and is passed to in-flight model/tool calls. A noncooperating
operation can delay pause or stop completion; neither operation kills arbitrary Go
code or rolls back side effects. Terminal states cannot resume.

Management tools accept callbacks in the same way as creation. The CLI root gets
stop, pause, resume, inspect, and list tools. Application wiring chooses their
controller operations. The tool package does not import agent/controller types.
Host terminal commands provide recovery when the root itself is paused.

## PDF data flow

```text
read_pdf(path, pages)  # absolute paths or relative to the tool working directory
  → bounded input snapshot
  → pdfinfo page count + pdftoppm page rendering
  → tool.Result { metadata, page label, PNG, page label, PNG, ... }
  → agent history (independent image copies)
  → provider.Submit
  → Chat Completions: correlated text tool results, then labeled image content
  → image-capable model reads the document and continues the same agent loop
```

Rendering is bounded by time, source bytes, rendered bytes, dimensions, and page
count. Page selections are one-based, unique, and ordered. Default batches report
the omitted page count; there is no claim that a partial batch covers the whole
PDF. Failed rendering returns a tool error with no partial images. Temporary input
snapshots are removed on completion and cancellation. Poppler is an external
runtime dependency; the model is supplied by the application. This adds no second
model loop, OCR fallback, or general capabilities framework.

Tests cover idle and active pauses, remaining tool batches, deferred replies,
resume and terminal-stop behavior, callback argument validation, PDF page order,
limits and cancellation, real rendered images reaching HTTP, complete tool-batch
ordering, and image snapshot isolation.
