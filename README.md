# Strap

A small Go core for a persistent root agent, delegated agents, routed messages,
and model interaction, with shared plans and an implementation/audit/repair cycle.
The same agent loop serves the root and every child.

## Developer console

With your local model server running:

```sh
go run ./cmd/strap
```

Select your server with `-model` and `-base-url`; `-timeout` limits each model
request. The CLI defaults to `-backend vllm` with the `qwen3.6-coding` preset.
The server must support Chat Completions and tool calling. For a generic compatible
server, use `-backend chatcompletions`, which sends no generation overrides.
See `go run ./cmd/strap -help` for all options.

The Qwen preset follows the [Qwen3.6-35B-A3B precise-coding guidance](https://huggingface.co/Qwen/Qwen3.6-35B-A3B#best-practices):

| Setting | Value |
|---|---|
| Temperature / top-p / top-k | `0.6` / `0.95` / `20` |
| Min-p / presence penalty / repetition penalty | `0` / `0` / `1` |
| Maximum output tokens | `32768` |
| Thinking | Enabled |

Explicit flags override individual preset values. Use `-preset none` to leave
unspecified settings to the server, especially when selecting another model:

```sh
go run ./cmd/strap -temperature 0.4 -max-tokens 16384
go run ./cmd/strap -model another-model -preset none -top-p 0.9
```

Overrides include `-temperature`, `-top-p`, `-top-k`, `-min-p`,
`-presence-penalty`, `-repetition-penalty`, `-max-tokens`, and `-thinking=false`.
Zero and false are explicit values, not omissions. Presets and generation overrides
require the vLLM backend; the generic backend rejects them instead of ignoring them.
The same immutable provider settings apply to the root, implementors, and auditors.
Changing `-model` does not automatically change the selected preset.

The vLLM adapter targets the generation fields exposed by vLLM 0.25.0, including
`chat_template_kwargs.enable_thinking`. Thinking requires support in the served
model's template. Settings are sent on every request; unsupported settings and
output/context limits remain visible server errors, with no automatic retry or
parameter substitution. Reasoning-history preservation is not implemented.

Shell and file tools run in the current directory, or the directory selected with
`-C /path/to/project`. The CLI enables them for the root and implementors. Auditors have a separate
prompt and omit the write/edit-file tools; shell access still has host permissions.
The CLI also provides `read_pdf`; see the PDF example below for its image-model
and Poppler requirements.
`web_search` and `open_url` are enabled for all three agent roles; see
[Web research](#web-research) for their browser dependencies. Use `-web=false`
to omit them. Missing backends produce a tool error when called; startup does not
launch a browser.
Commands run with host permissions, without a sandbox or approval prompt.

Type a message and press Enter. Input stays available while agents work. The
colorized transcript shows tool calls with the calling agent. Consecutive calls
collapse into one line, grouped by agent and tool name with repeat counts:
`agent-2 · Read file ×3, Write file`. A conversation message or error starts a new
group. Long rows end with an ellipsis to fit the terminal width.
Agent creation, assignments, messages, and errors remain visible. Raw tool arguments, call
IDs, and result payloads stay out of the display. Routine delivery receipts are
tracked internally; they are not printed as conversation output.

Conversation messages render Markdown with headings, emphasis, lists, links,
tables, and code blocks using [Glamour](https://github.com/charmbracelet/glamour).
A dedicated light/dark theme gives all six heading levels clear hierarchy without
literal hash prefixes, frames syntax-highlighted code blocks, and styles nested
lists, task lists, quotes, links, strikethrough, dividers, and definition lists.
Formatting adapts to terminal width and color support, including `NO_COLOR`.
Source messages remain unchanged; rendering is cached until the width changes.

After an exchange, `Idle` means the agent is waiting for another message.
`queued` counts pending messages; it is a delivery status, not an agent state.

An animated spinner tracks the current active period, including delegated work,
with separate elapsed times for running tools. The last active duration remains
visible when idle. Assistant text accompanying tool calls appears as an attributed
progress paragraph before that batch's tool rows. Agents are prompted to explain
their first action and meaningful findings between batches. These host-only
updates do not enter agent inboxes or mark work complete; the original text stays
in the generating agent's assistant history without an additional message.
Replies and progress text appear when each model response completes; token
streaming is not implemented.

After a tool batch finishes, its tool line shows the agent's context size, for
example `agent-1 · Read file ×2 · 12,345 context tokens`. The TUI counts that exact
history snapshot through the provider, including system instructions, tool
definitions, messages, and the completed tool results. Grouped tool lines show
the latest completed batch's count, not a sum or the size of tool output alone.
Counting runs in the background with a ten-second timeout. Unsupported providers
and counting failures show `context tokens unavailable`; agents keep running.

`/agents` shows each agent's state, parent, context size, last-call output tokens,
and configured output cap per model call. Context is counted in the background
from each agent's history snapshot when you run the command, including ordinary
replies and completed tool results. Run `/agents` again to refresh. The cap resets
each call; output from previous calls does not reduce it. `—` means no call has
returned yet; `unknown` means a count or cap is unavailable, including server
defaults. Narrow terminals show labeled rows instead of columns. The CLI currently
shares one provider configuration, so its agents share a cap while their usage
and context sizes differ.

Scroll with the mouse wheel, trackpad, or Page Up / Page Down to browse conversation
history. New output preserves your position while you read. The footer shows your
position in history; Ctrl-End returns to the latest output and resumes following it.

Drag with the mouse to select visible text; releasing the mouse copies it to the
system clipboard. Selection holds the visible screen steady while agents and
event collection continue. Escape, scrolling, or typing returns to the live view.
Ctrl-C copies again while text is selected; otherwise it exits as usual. The same
selection behavior works in the transcript browser.

For native terminal selection, press F2 to freeze the display and release mouse
capture, then drag and use your terminal’s Copy shortcut (usually Cmd-C on macOS
or Ctrl-Shift-C on Linux). This is also a fallback when the system clipboard is
unavailable. Press F2 again to resume. Typing is suspended in F2 mode, preserving
your draft; keyboard scrolling remains available.

Type `/` to browse slash commands, or keep typing to filter them. Use Up / Down
to select and Tab to complete. Enter on a partial command fills it in; Enter on
a complete command runs it. Escape dismisses suggestions. Commands that accept
an agent ID leave space to type the argument after completion.

Enter sends the entire draft. Alt+Enter or Ctrl+J inserts a newline; multiline
paste stays in the draft until you send it. The input grows up to six visible
rows and scrolls to keep the cursor visible. Up / Down moves within multiline
or wrapped drafts; Alt+Up / Alt+Down recalls message history and restores your
unfinished draft. Single-line drafts also retain Up / Down history navigation.
Tab inserts four spaces outside slash completion. Slash commands run only from
a single-line draft, so pasted multiline text beginning with `/` is sent as a
message. Leading indentation and trailing newlines are preserved when sending.

| Key or command | Action |
|---|---|
| Enter | Send the draft, or complete / run a slash command |
| Alt+Enter / Ctrl+J | Insert a newline |
| Up / Down | Select a suggestion, move within multiline input, or recall single-line history |
| Alt+Up / Alt+Down | Recall history / restore the unfinished draft |
| Tab / Escape | Complete / dismiss suggestions; Tab otherwise inserts four spaces |
| Mouse wheel / trackpad / Page Up / Page Down | Scroll the transcript |
| Ctrl-Home / Ctrl-End | Jump to the beginning / end |
| F2 | Freeze / resume display updates for copying |
| Mouse drag, then release | Select and copy visible text to the clipboard |
| F2, then mouse drag + terminal Copy | Select and copy visible text |
| `/agents` | Show per-agent state, context tokens, last output, and output cap |
| `/inspect [id]` | Inspect agent state; defaults to root |
| `/transcript [id]` | Browse an agent’s actual conversation; defaults to root |
| `/pause [id]` | Pause at an operation boundary; defaults to root |
| `/resume [id]` | Resume a paused agent; defaults to root |
| `/stop [id]` | Permanently stop an agent; defaults to root |
| `/clear` | Clear the display, keeping conversation context |
| `/help` | Show commands |
| `/quit`, Ctrl-C, Ctrl-D | Cancel the conversation and exit |

The transcript browser reads the same history used for model requests. It starts
with the latest 20 messages, including system instructions, received messages,
assistant text and tool calls, and tool results. Scrolling up or pressing Page Up
above the loaded beginning fetches older messages. Use `[` / `]` to switch agents,
`r` to refresh the snapshot, `v` to toggle formatted/raw fields, and Escape to return to the main conversation.
Raw fields preserve message envelopes and represent tool arguments as strings;
images display metadata rather than binary data. Browsing does not send messages
or call a model. Agents and the main conversation continue running in the background.
The main input draft and scroll position are preserved.

Sessions live in memory. If the root exits after a provider error, restart the
CLI to begin a new conversation.

Requires Go 1.24 or later. The terminal UI uses Bubble Tea; the conversation core
and HTTP provider use the standard library.

The examples remain available:

```sh
go run ./examples/delegation  # Scripted audit/repair cycle; no server required.
go run ./examples/local       # Real local-model audited delegation.
go test -race ./...
```

## Package map

```text
cmd/strap/     CLI flags, provider setup, conversation lifetime
internal/tui/  Terminal input, transcript, host event rendering
conversation/  Controller: creates agents, binds senders, routes messages,
               tracks receipts, publishes host events, joins agent shutdown
agent/         Persistent, sequential inbox → model → tool loop
inbox/         In-memory FIFO; a channel wakes the single consumer
identity/      Shared actor identity without transport dependencies
work/          Plans, scoped work, submissions, audits, and pending work events
internal/workflow/  Application-owned work dispatch and agent configurations
message/       Envelopes, work snapshots, outgoing drafts, delivery receipts
prompt/        Structured operating instructions and JSON rendering
content/       Ordered text/image parts with independent image snapshots
provider/      Submit interface, model history, responses, tool-call wire types
  chatcompletions/  HTTP adapter for a configured model server
tool/          Runtime Tool interface, generic Func and compiled input contracts,
               shell execution and text file operations
examples/      Delegation demos and direct shell/file tool usage
```

## Plans and audited work

Run the scripted full cycle without a server:

```sh
go run ./examples/delegation
go test -race ./...
```

The example delegates two of three plan steps, fails the first audit, sends the
implementor a scoped repair, and accepts the repaired submission after another
audit. The third step stays pending. `go run ./examples/local` exercises the same
submission/audit tools against a real model server for a standalone arithmetic task.

The CLI exposes tools according to each agent’s role:

| Tool | Contract |
|---|---|
| `update_plan` | Root: omit IDs to create, or use `plan_id` and `expected_revision` to edit structure. Implementor: use `work_id` and `expected_revision` for scoped progress |
| `update_work` | Auditor reports work-level notes and blockers without changing implementation steps |
| `assign_work` | Assign implementation with task and optional scope, or audit with original work ID, revision, and submission ID; application selects agent configuration |
| `get_plan` / `get_work` | Read current authorized snapshots, scoped steps, and available submission/repair findings |
| `get_audit` | Read an immutable verdict, summary, and findings using the event's `audit_id` |
| `submit_work` | Implementor or repair actor submits an outcome for review |
| `submit_audit` | Auditor records pass or fail; failure creates scoped repair work |
| `reassign_work` / `cancel_work` | Owner recovery; omit reassignment's `assignee` to provision a replacement with the appropriate configuration |

Implementors receive progress/read tools and `submit_work`. Auditors receive
work reporting/read tools and `submit_audit`. They cannot provision arbitrary agents or
issue arbitrary assignments. A work blocker reports missing inputs or unavailable
verification; it does not produce a failing verdict.

Steps move through `pending`, `in_progress`, `blocked`, `ready_for_review`,
`completed`, or `cancelled`. The implementor reports readiness; only an audit can
complete delegated steps. Text replies and delivery receipts do not imply submission
or acceptance. The root receives actionable review/blocker events; ordinary progress
updates its context without initiating a model call by itself.

## Library setup

`harness.New` assembles the same prompts, role tools, providers, and audited work
used by the CLI. Each session owns its HTTP connection pool and local resources:

```go
cfg := harness.DefaultConfig()
cfg.Dir = projectDir
cfg.Model.BaseURL = "http://localhost:8000"
session, err := harness.New(ctx, cfg, harness.Dependencies{})
if err != nil {
    return err
}
defer session.Close(context.Background())
_, err = session.Send(session.Root(), "Inspect this project.")
```

Import `github.com/stevemurr/strap/harness`. `Dependencies` accepts borrowed
providers/tools for tests and explicit owned resources. `StartupError` retains a
retryable cleanup handle if construction rollback fails. The current `NextEvent`
stream is available through independent `session.Subscribe(after)` readers and
finite `session.Events(ctx, query)` pages, as described in
[the harness design](HARNESS_DESIGN.md). `Close(ctx)` rejects new commands,
cancels execution, drains final events, then closes owned resources. Its context
limits the caller's wait, not cleanup. A cleanup error preserves `Closing` state;
call `Close` again to retry unfinished resources. Inspection remains available.
Automatic token counting belongs to the session (`cfg.Telemetry`), so attaching
or detaching a view does not change provider traffic. Set `ContextTokens = false`
to disable it. `Configuration()` shows resolved role configuration and marks
injected providers as opaque. `Inspect()` includes capture coverage and health.

For evals, choose a completion rule explicitly: a root reply, idle agent, consumed
receipt, accepted work, and closed session are different facts. Grade the domain
result, call `Close` to finalize evidence, inspect capture errors/omissions and
retained cursor bounds, then `Dispose` after reading. Current capture includes
domain events and tool diagnostics; it does not include full model requests,
responses, or external artifacts. Compare causal events rather than assuming
identical total ordering across concurrent runs.

The [HTTP adapter](harness/httpapi/README.md) exposes these same operations. Run
`strap -listen 127.0.0.1:8080` with `STRAP_API_TOKEN` set, or embed the authorized
`httpapi.Service` handler. Each connection has its own event cursor.

For durable diagnostics, set `cfg.Events.JSONLPath` or pass `-record trace.jsonl`
to the CLI. Recording exclusively creates a new file and includes tool arguments,
results, errors, and exact edit-failure snapshots. Traces contain task/file data.
`eventlog.OpenJSONL(ctx, path)` inspects sealed or interrupted traces without
resuming execution. Successful session closure syncs a JSONL trace.

Call `Dispose(ctx)` when finished reading to release event storage. `Capture()`
reports omissions and capture failures separately from execution state. Memory
retention is bounded; an expired cursor is an explicit error. Lifecycle state
revisions are separate from model history revisions.

The public `work` package can be used independently of agents and transport:

```go
store := work.New()
title, stepTitle := "Storage", "Propagate write errors"
plan, err := store.UpdatePlan("root", work.PlanUpdate{
    Title: &title,
    Steps: []work.StepEdit{{Title: &stepTitle}},
})
if err != nil {
    return err
}
assigned, err := store.AssignWork("root", work.AssignRequest{
    Assignee: "implementor",
    Task: "Implement and verify storage error handling.",
    Scope: &work.Scope{PlanID: plan.ID, StepIDs: []work.StepID{plan.Steps[0].ID}},
})
if err != nil {
    return err
}
_ = assigned // Deliver its independent snapshot to the assignee.
```

In a live conversation, use actual actor IDs from `conversation.Controller`.
Create idle agents with explicit `agent.Spec{Provider, Prompt, Tools}`, register
work through the store, then deliver its snapshot. `message.Message` and
`message.Draft` carry `Work *work.Work`; `message.Assignment` has been removed.
Runtime `tool.Call.Actor` supplies the caller identity. Neither model arguments nor
forwarded work snapshots grant write permissions.

Tool constructors take application callbacks; for example, `tool.UpdatePlan`
takes separate plan-edit and work-progress handlers. `work.Store` owns validation,
revisions, scope reservations, and immutable outcomes. It never creates agents or
sends messages. The application's dispatcher handles its pending events and
configures auditors independently from implementors.

See [the CLI wiring](cmd/strap/main.go), [the application dispatcher](internal/workflow/session.go),
and [the work contract](WORK_DESIGN.md). The dispatcher consumes the controller's
single event stream and relays it to the UI, so work continues without a UI reader.
`conversation.Deliver` is a trusted host operation; model-facing messaging still
uses the runtime-bound `Sender`.

`CreateAgent(handle)` remains a low-level compatibility callback for hosts and
core transport tests. It accepts only task/context/expected-output arguments and
confers no work-store authority. It is not installed in the CLI; tracked delegation
uses `assign_work`. Custom hosts retaining that callback must register work before
delivering its authoritative snapshot.

Creation and reassignment are not transparently retried. Work revisions prevent
stale writes; exact submission identity prevents stale audits. Owner notification
events remain pending until consumed; definite delivery failures are reported for
explicit recovery. There is no persistence or exactly-once delivery promise.
Revisions fence ledger mutations, not shell execution or shared filesystem writes.
Artifact references identify outputs to verify; resolving or isolating them belongs
to application tooling.

## Agent management

The application connects ordinary tools to controller operations; `tool` imports
neither `conversation` nor `agent`. `StopAgent`, `PauseAgent`, and `ResumeAgent` accept a callback with this signature:

```go
func(context.Context, tool.Call, message.ActorID) (tool.Result, error)
```

`InspectAgent` accepts
`func(context.Context, tool.Call, tool.InspectAgentArgs) (tool.Result, error)`.
Its arguments are `agent_id`, optional `limit` (1–100, default 20), and optional
`before` (exclusive, one-based message position). The CLI returns state and a
chronological transcript projection with message positions and `has_earlier`.
To page backwards, pass the first returned position as `before`. Model-facing
text is limited to 4 KiB per entry and a 32 KiB encoded-entry budget per response;
shortened entries are marked `truncated`, and images are labeled as metadata.
The underlying transcript remains intact, including prior inspection results.

`ListAgents` accepts `func(context.Context, tool.Call) (tool.Result, error)`.
See [harness/agents.go](harness/agents.go) for application wiring. These tools
are selected only for the CLI root. Execution agents still have local and messaging
tools. Host controls are available for every agent, including a paused root.

The controller's `PauseAgent`, `ResumeAgent`, and `StopAgent` return
`(AgentInfo, error)`. `AgentInfo` contains `agent_id`, `parent`, and `state`.
`InspectAgent(id, InspectOptions)` returns `(AgentInspection, error)`, preserving
those fields, including a `UsageSnapshot`, and optionally a `TranscriptPage`. Set
`InspectOptions.Transcript` to an `agent.TranscriptQuery` to request history;
leave it nil for state and usage only. Controller snapshots are independent copies of the
actual stored messages, including image bytes. State, usage, and history are nearby
snapshots, not an atomic execution checkpoint. Queued inbox messages appear only
after the agent consumes them. Stopped agents remain inspectable during the session.
No transcript tee, event reconstruction, or separate history store is involved.
`Agents()` returns the current snapshots. `AgentStateChanged` publishes ordered
state transitions to the host event stream.

Token usage is reported per model call through `provider.Response.Usage`, with
optional `InputTokens` and `OutputTokens` counts. Nil means unavailable; reported
zero remains zero. `Agent.Usage()` and `AgentInspection.Usage` expose cumulative
reported totals, missing-report counts, and the latest call observation, including
after an agent exits. `conversation.UsageEvent` publishes each returned call to
the host. Accounting includes valid usage from rejected completions. The latest
input count measures the submitted history revision; cumulative usage does not
measure the current context. The TUI's tool-line context counts are separate
measurements. Token estimation, compaction, and cumulative usage display are
not implemented.

To count an assembled request before generation, use the optional
`provider.TokenCounter` capability. The vLLM client implements it:

```go
count, err := client.CountTokens(ctx, request) // client is *vllm.Client
if err != nil {
    return err
}
fmt.Printf("Request input: %d tokens\n", count)
```

Counting calls vLLM's [`POST /tokenize`](https://github.com/vllm-project/vllm/blob/v0.25.0/vllm/entrypoints/serve/tokenize/protocol.py)
with the same model, translated messages (including images and tool results),
tool definitions, and thinking settings used for generation. The server applies
its chat template and tokenizer, including the generation prompt. The returned
count excludes future output and is not accumulated usage. Count again when the
request changes. Invalid counts and unavailable endpoints return errors, with no
local estimate or automatic retry.

A base URL ending in `/v1` uses the sibling `/tokenize` endpoint; preceding proxy
paths are preserved (`/proxy/v1` becomes `/proxy/tokenize`). Counting is explicit:
`Submit` does not call it automatically, and the generic Chat Completions adapter
does not implement `TokenCounter`. Callers holding a `provider.Provider` can check
support with a type assertion to `provider.TokenCounter`.

`conversation.ToolBatchEvent` identifies an agent's completed tool-call IDs and
history revision. `CountAgentTokens(ctx, agentID, revision)` counts that exact
revision with the agent's own provider and tool definitions, including after the
agent advances or stops. The TUI calls this asynchronously once per displayed
batch. Counting never updates generation usage totals.

| State | Meaning |
|---|---|
| `idle` | Waiting for input |
| `running` | Advancing the model/tool loop |
| `pause_requested` | Pause accepted; the current operation may still be running |
| `paused` | Parked at a boundary; no further tools, model calls, replies, or input consumption |
| `stop_requested` | Cancellation requested; loop exit is still pending |
| `stopped` | Loop ended normally or through cancellation |
| `failed` | Loop ended with a provider or runtime failure |

Pause finishes and records an in-flight model request or tool invocation before
parking. Remaining calls in a tool batch and pending replies resume exactly where
the loop stopped. Incoming messages stay queued. Resume also retracts a pending
pause. Stop is terminal; it does not undo completed side effects. `AgentExited`
confirms loop exit and follows reporting of unconsumed messages. Repeated stop
requests are harmless; stopped/failed agents cannot resume. Stopping a parent
does not stop its children; conversation close cancels and joins every agent.

## Web research

`web_search` searches DuckDuckGo through **wkrender**, the native macOS WebKit
renderer. It returns ranked titles, destination URLs and snippets. `open_url`
uses **agent-browser 0.37.1** to load a page in its own headless Chrome session
and read the rendered DOM. It returns readable text and a separate list of link
destinations. Both use ordinary tool activity and agent commentary in the console.

Install wkrender from the neighboring checkout on macOS (Swift/Xcode required):

```sh
make -C ../wkrender install
```

The default location is `~/.harness/bin/wkrender`. Strap requires worker protocol
1, four concurrent slots, and search readiness. It keeps the worker warm and
cancels individual searches independently. Search challenges and unknown result
markup are errors; only an explicit no-results page produces an empty list.
There is no HTTP search fallback.

Install the pinned agent-browser package in an isolated directory:

```sh
npm install --prefix "$HOME/.local/share/strap/agent-browser" --save-exact agent-browser@0.37.1
```

Strap discovers the packaged native executable there if `agent-browser` is not
on PATH. The package's installer declares Node 24+; the native browser runtime
does not need Node. On macOS, Strap uses the installed Google Chrome executable
when present. Otherwise install Chrome through agent-browser (`agent-browser
install` using your installed executable), or provide a path explicitly. See the
[upstream installation instructions](https://agent-browser.dev/installation).

```sh
go run ./cmd/strap -wkrender /path/to/wkrender -agent-browser /path/to/agent-browser
go run ./cmd/strap -browser-executable /path/to/chrome
go run ./cmd/strap -web=false
```

Executable paths and backend selection belong to the host. The model sees only:

```json
{"query":"Go context package documentation","max_results":8}
```

```json
{"url":"https://pkg.go.dev/context","max_chars":20000}
```

Search returns `query` and `results: [{title, url, snippet}]`, with at most ten
hits. Page reads return `url`, `final_url`, `title`, `content_type`, `content`,
`links`, `truncated`, and `document_truncated`. A `links_truncated` flag marks
omitted link metadata. The page reader supports HTML and text, not PDF/image
extraction or interactive browser actions.

When `truncated` is true, continue with the returned `next_cursor`:

```json
{"url":"https://pkg.go.dev/context","cursor":"<next_cursor>","max_chars":20000}
```

Continuation reads an immutable cached text snapshot without another navigation.
The cursor belongs to the calling agent and the original requested URL. Snapshots
expire after ten minutes or are evicted for space; an unavailable cursor requires
reopening the URL without it. Share URLs, not cursors, with other agents.
`document_truncated` separately means the backend or retention limit discarded
the document's tail; a continuation cannot recover that tail.

Defaults are a 20-second search budget, a 30-second page-read budget (both include
queuing/startup), four concurrent searches, two concurrent browser reads, 20,000
characters per page chunk (200–50,000 allowed), one million retained characters
per document, and a 16 MiB/64-snapshot cache. Returned link metadata is bounded to
16 KiB. Browser cleanup has a separate five-second budget.

Browser reads use fresh profiles and private socket directories, with no user
profile restoration or ambient agent-browser configuration. The adapter uses
`open <url>` then bare `read`; `read <url>` would fetch directly rather than read
the rendered page. A bounded readiness check handles initially empty/loading
pages. Temporary browser sessions close after extraction, including on failure
or cancellation. The pinned Unix adapter can stop its owned browser/daemon
processes if a stuck navigation prevents normal close. WebKit requires macOS;
the agent-browser adapter is exercised on macOS and has Unix cleanup for Linux.
These tools have host network access, including local development URLs.

Library hosts construct `tool.NewWeb(tool.WebConfig{...})`, share `web.Tools()`
with their agents, and call `web.Close(cleanupCtx)` after stopping those agents.
The CLI performs that cleanup. Browser state and snapshots stay outside model
history; returned search/page text enters history as ordinary tool results.
Agent prompts direct the model to read primary sources, cite actual source URLs,
and treat retrieved content as evidence rather than instructions.

Offline fixtures cover worker failures, cancellation, browser command isolation,
result parsing, cache bounds, Unicode continuation, and cursor ownership. Real
browser checks are opt-in:

```sh
go test -race ./...
sh scripts/check-web-coverage.sh
STRAP_LIVE_WEB=1 go test -race ./tool -run TestLiveWeb -count=1 -v
STRAP_LIVE_WEB=1 STRAP_LIVE_BASE_URL=http://localhost:8000 go test ./cmd/strap -run TestLiveWebResearch -count=1 -v
```

The coverage check requires 100% Go statement coverage for `web_search.go`,
`open_url.go`, the shared `web.go` runtime, and all compiled files in
`internal/agentbrowser`, `internal/webkit`, and `internal/webprocess`. It runs
offline tests with the race detector; live browser behavior and embedded DOM
scripts are validated by the separate opt-in checks below.

Backend live checks use a local JavaScript page, redirects, long text, concurrent
readers, cancelled navigation, and two external searches. The model check asks
the configured server to search, read, and cite an official source. Optional executable overrides
are `STRAP_WKRENDER`, `STRAP_AGENT_BROWSER`, and `STRAP_BROWSER_EXECUTABLE`.

## PDF reading and image results

```sh
go run ./examples/pdf -file tool/testdata/pages.pdf
go run ./examples/pdf -file /path/to/report.pdf -model qwen/qwen3-vl-4b
```

`read_pdf` renders page images and returns them to the calling agent. The same
provider submits those images on its next turn; the tool never calls a model.
Poppler must be installed. A missing renderer or a server that rejects image input
returns an error; no text-extraction fallback silently replaces the visual input.

```json
{"path":"report.pdf","pages":[1,2,3]}
```

Pages are one-based and returned in the requested order. By default, a call returns
the first six pages. Its metadata reports `path`, `total_pages`, `pages`, and
`omitted_pages`; request more pages in later calls. Explicit empty, duplicate,
out-of-range, or excessive page selections are rejected.

`tool.NewPDF(tool.PDFConfig{Dir: projectDir})` defaults to six pages per call,
32 MiB input, 16 MiB combined PNG output, a 1,600-pixel longest edge, and a
30-second rendering deadline. Limits are configurable; page count is restricted
to 1–32 and dimensions to 64–4,096 pixels. Absolute PDF paths are opened directly;
relative paths resolve from `Dir` (the current directory by default). Parent-directory
paths and symlinks work normally; the target must be a regular file. For example,
`{"path":"/Users/me/Documents/report.pdf","pages":[1]}` needs no relative conversion.
The tool snapshots its input and removes temporary files after rendering.

Tools now return `tool.Result{Content: content.Content}`. `tool.Text(text)` and
`tool.JSON(value)` produce ordinary text results. `Content` contains ordered parts:

```go
content.Content{
    {Text: "report.pdf, page 2 of 8"},
    {Image: &content.Image{MIMEType: "image/png", Data: pngBytes}},
}
```

`provider.Message.Content` uses the same type. Tool results and provider requests
copy image bytes so neither a tool nor a provider can mutate retained history.
The Chat Completions adapter keeps all tool results contiguous with their call
IDs, then sends labeled images as user content after the batch. This is an adapter
translation: internal history retains the images on the tool result. The
[Chat Completions protocol](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
permits images in user content and text only in tool messages.

## Shell and files

Run the standalone examples without a model server or credentials:

```sh
go run ./examples/shell
go run ./examples/files
```

Both print the JSON calls and results, work in a temporary directory, and remove
that directory when finished. The [shell example](examples/shell/main.go) shows
combined output, a nonzero exit, truncation, and a per-call timeout (macOS/Linux).
The [file example](examples/files/main.go) writes text, reads a line window, and
recovers from an ambiguous edit by supplying a unique match. Their source also
shows how to decode typed results for use by a host application.

Library hosts opt into local tools when constructing an agent:

```go
shell, err := tool.NewShell(tool.ShellConfig{Dir: projectDir})
if err != nil {
    return err
}
files, err := tool.NewFiles(tool.FilesConfig{Dir: projectDir})
if err != nil {
    return err
}
spec := agent.Spec{
    Provider: client,
    Prompt: prompt.Prompt{Role: "Inspect and update the project using the available tools."},
    Tools: append([]tool.Tool{shell}, files.Tools()...),
}
```

Import `github.com/stevemurr/strap/tool`. Share a single `Files` instance between
agents using the same directory; its adapters serialize direct file operations.

| Tool | Arguments | Result |
|---|---|---|
| `shell` | `command`, optional `timeout_ms` | Combined output, exit code, timeout and truncation flags |
| `read_file` | `path`, optional `offset`, `limit` | Numbered text, total lines, more/truncation flags |
| `write_file` | `path`, `content` | Path and bytes written |
| `edit_file` | `path`, `old`, `new` | Path and one replacement |

Shell execution supports macOS and Linux. Each call starts a fresh shell, defaults
to a 30-second timeout, accepts at most 5 minutes, and retains the first and last
halves of 64 KiB of output. Nonzero command exits are normal JSON results; failure
to start is a tool error. Timeout results retain captured output. Cancellation and
normal completion stop remaining processes in the command's process group. There
are no managed background jobs; programs that leave the group are outside this
cleanup mechanism. The default environment includes only `PATH`, `HOME`, `TMPDIR`,
`LANG`, `LC_ALL`, and `TERM`; hosts may supply an explicit environment and limits.

File tools accept absolute paths directly and resolve relative paths from the
configured directory, including `..` paths. Symlinks resolve to their targets;
new files may be created through symlinked parent directories. Existing targets
must be regular files. Dangling symlinks and symlink cycles return errors.
Text must be UTF-8 without NUL bytes and fit within 1 MiB by default. Reads default to 200 lines, allow up to 2,000 per call, and cap returned
text at 64 KiB. `more` indicates additional content; `truncated` specifically means
the byte cap cut the requested window. Edits require a unique, nonempty exact
match. Empty `content` clears a file; empty `new` deletes the matched text.

Writes use a same-directory temporary file and rename, retaining existing regular
permission bits; new files are private (`0600`). Parent directories must exist.
For a symlinked file, replacement updates its resolved target and preserves the
symlink itself. Result paths retain the caller’s supplied path.
Replacement changes inode identity and does not preserve ownership, extended
attributes, or hard-link relationships. Atomic replacement prevents partial
contents becoming visible; it does not promise crash durability or detect external
edits. File locking covers this `Files` instance only. Path checks are not a
sandbox against concurrent filesystem changes, and shell access remains unrestricted.

## Current scope

The public session API and adapter-independent architecture are tracked in
[the audited harness design](HARNESS_DESIGN.md). Shared typed workflow operations
back both the model tools and the public `harness.Session` API. Session assembly
and resource ownership are implemented; event storage and lifecycle changes are
being introduced in separate stages.

This is an executable design scaffold. Messages, receipts, and history are in
memory; persistence, deduplication, streaming, context compaction, and admission
policies are deferred. There is no behavior framework, permission stack, workspace
model, or configurable workflow engine. Work revisions validate ledger mutations;
they do not gate general model responses or local-tool execution.

The root's delegation role is expressed through its instructions and supplied
tools. Queues and histories are unbounded in this first core. Models and tools must
honor cancellation and be safe for concurrent use if shared between agents.

See [DESIGN.md](DESIGN.md) for the complete flow and interface boundaries, and
[the conversation tests](conversation/controller_test.go) for controlled interleavings.

## Defining tools

Keep the non-generic `tool.Tool` interface for the runtime registry. Define tools
with `tool.Func[A]`, whose `tool.Definition[A]` and `tool.Handler[A]` use the same
argument type. `tool.NewParameters[A]` compiles the JSON field structure and
constraints once; schema generation and runtime decoding use that one contract.
See [the tool contract design](DESIGN.md#typed-tool-contracts) and
[the executable example](tool/example_test.go).
