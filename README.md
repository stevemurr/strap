# Strap

A small Go core for a persistent root agent, delegated agents, routed messages,
and model interaction, with shared plans and an implementation/audit/repair cycle.
The same agent loop serves the root and every child.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/stevemurr/strap/main/install.sh | sh
```

That fetches the latest release for this machine (linux/amd64, macOS arm64 or
amd64) and installs `strap` into the first writable of
`~/.local/bin` or `/usr/local/bin`. `STRAP_VERSION=v1.2.3` pins a tag and
`STRAP_BIN=/somewhere/bin` chooses the directory. With a Go toolchain,
`go install github.com/stevemurr/strap/cmd/strap@latest` does the same job.

## Pointing it at a model

Strap ships no endpoints. It reads `~/.config/strap/models.json` (or
`$XDG_CONFIG_HOME/strap/models.json`), and falls back to a bundled catalog whose
profiles all point at `http://127.0.0.1:8000`, which is useful only if you serve
a model locally. Keeping your own file out of the repository is the point: it
holds host names you may not want to publish, and it takes effect on the next
invocation without rebuilding.

```jsonc
{
  "default": "qwen3.6",
  "models": {
    "qwen3.6": {
      "backend": "vllm",                 // vllm or chatcompletions
      "base_url": "https://model.example:8443",
      "model": "qwen3.6",                // the alias the server advertises
      "timeout": "60m",
      "generation": { "temperature": 1.0, "top_p": 0.95, "max_tokens": 81920 }
    }
  }
}
```

A personal file replaces the whole catalog: `default` picks the profile that
runs without flags, and each entry under `models` pairs a server's model alias
with its endpoint, request timeout, and generation settings. `-config
/path/to/models.json` selects a different file, and `-profile <name>` a
different entry. [`internal/modelcatalog/models.json`](internal/modelcatalog/models.json)
is a starting template to copy.

## Developer console

With your model server reachable:

```sh
strap                      # or: go run ./cmd/strap
```

```sh
go run ./cmd/strap -profile qwen3.6
go run ./cmd/strap -config ~/.config/strap/models.json -profile nemotron-lightning
go run ./cmd/strap -temperature 0.8 -max-tokens 16384
```

Flags override the selected profile, regardless of argument order. `-model` changes
only the server alias; `-profile` selects the saved settings. `-base-url` and
`-timeout` override the endpoint and per-request timeout. Profile `timeout` values
use Go durations such as `60m`; omitted timeouts use the harness default (one hour).
A missing profile `backend` means `vllm`; a missing `generation` block leaves every
sampling setting to the server. A profile states its complete generation policy;
there are no named presets in code.
Unknown JSON fields and invalid selected model settings fail before startup.
See `go run ./cmd/strap -help` for all options.

The Nemotron profile assumes the alias serves **NVIDIA Nemotron 3.5 Lightning
30B-A3B**. Its [Hugging Face card](https://huggingface.co/nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-BF16#api-client)
and NVIDIA's [SWE-bench recipe](https://github.com/NVIDIA-NeMo/Gym/blob/main/nemotron_recipes/lightning-3.5/instruct/nemo-evaluator/swebench-verified.yaml)
support this coding baseline (checked September 12, 2026):

| Setting | Nemotron Lightning |
|---|---|
| Temperature / top-p | `1.0` / `0.95` |
| Thinking | Enabled |
| `force_nonempty_content` | Enabled, as the card recommends for coding agents |
| Top-k, min-p, presence/repetition penalties | Unspecified; server defaults |
| Maximum output tokens | Unspecified; set `-max-tokens` for your deployment |

These are published recommendations, not a demonstrated optimum for every coding
task. The card's `16000` output limit is an API example; NVIDIA's agentic coding
recipes drop `max_tokens` before sending requests. The profile therefore does not
inherit Qwen's 128K output cap. For vLLM tool calling, the card specifies
`--enable-auto-tool-choice --tool-call-parser qwen3_coder --reasoning-parser nemotron_v3`.
Strap sends successful reasoning history back to the server; the served chat
template controls its inclusion in the prompt. These sampling settings alone do
not reproduce NVIDIA's benchmark setup.

The Qwen profiles spell out the model card's coding settings: temperature `0.6`,
top-p `0.95`, top-k `20`, min-p `0`, presence penalty `0`, repetition penalty `1`,
`131072` maximum output tokens, and thinking enabled (`qwen3.6`) or disabled
(`qwen3.6-nothink`). Library callers of `harness.DefaultConfig()` get server
defaults unless they set `Generation`; only the CLI loads catalogs.

Three Qwen3.8 Flash Next profiles target `qwen3.8-flash-next-mtp3`, each with its
generation policy stated in the catalog:

| Profile | Thinking | Temperature / top-p | Max tokens |
|---|---|---|---|
| `qwen3.8-flash-next-nothink` | Off | `0.7` / `0.80` | `32000` |
| `qwen3.8-flash-next-thinking` | On, `medium` effort | `1.0` / `0.95` | `32000` |
| `qwen3.8-flash-next-stream` | Off | Server defaults | `400` |

All Strap requests already stream, including the first two profiles. The stream
profile keeps the short output budget and unspecified sampling from the example.

Two profiles target the dense **Qwen3.8-27B** at `http://model.internal:8360` with
the sampling its [model card](https://huggingface.co/Qwen/Qwen3.8-27B) recommends
(checked September 16, 2026):

| Profile | Thinking | Temperature / top-p / top-k | Presence penalty | Max tokens |
|---|---|---|---|---|
| `qwen3.8-27b` | On, `xhigh` effort | `1.0` / `0.95` / `20` | `0` | `131072` |
| `qwen3.8-27b-nothink` | Off | `0.7` / `0.80` / `20` | `1.5` | `131072` |

Both set min-p `0` and repetition penalty `1`. The card suggests generous output
budgets for agentic work (up to `262144` reasoning tokens and `131072` response
tokens); the profiles use `131072` for the whole call, and the harness's
reasoning limit cuts off a call that reasons past 192 KB regardless.
Use `-reasoning-effort low`, `medium`, or `xhigh` to override the profile's effort;
the value is sent inside `chat_template_kwargs`. Configure the server with
`--reasoning-parser qwen3` to return reasoning separately from answer content.
Both `strap` and `strap eval` accept these profiles and generation overrides:

```sh
go run ./cmd/strap -profile qwen3.8-flash-next-nothink
go run ./cmd/strap -profile qwen3.8-flash-next-thinking -reasoning-effort medium
go run ./cmd/strap -profile qwen3.8-flash-next-stream
```

Generation flags override the selected profile's values, including explicit `0`
and `false`; a profile without a `generation` block starts from server defaults.
For example:

```sh
go run ./cmd/strap -model another-model -top-p 0.9
```

Overrides include `-temperature`, `-top-p`, `-top-k`, `-min-p`,
`-presence-penalty`, `-repetition-penalty`, `-max-tokens`, `-thinking=false`,
`-preserve-thinking=false`, `-reasoning-effort`, and
`-force-nonempty-content=false`. They require the vLLM backend. Explicitly selecting
`-backend chatcompletions` clears saved generation settings and uses server defaults;
explicit generation flags are then rejected.

To preserve Qwen reasoning across user turns, add `"preserve_thinking": true`
under your profile's `generation` object, or pass `-preserve-thinking`. All bundled
Qwen profiles enable it. Omission leaves the server default: Qwen3.6 defaults to
preserving reasoning only within the latest user turn; Qwen3.8 preserves all turns.
Explicit `false` selects the latest-user-turn policy, including intermediate tool
calls. See [Qwen3.6's guidance](https://huggingface.co/Qwen/Qwen3.6-27B#preserve-thinking)
and [Qwen3.8's guidance](https://huggingface.co/Qwen/Qwen3.8-27B#disable-preserved-thinking).

Both backends resend reasoning from successful assistant responses, including tool
calls, separately from answer content. Following [Qwen's client example](https://huggingface.co/Qwen/Qwen3.8-27B#text-only-input),
requests include identical `reasoning` and `reasoning_content` fields for server
compatibility. Reasoning is sent even when preservation is omitted or false so
the server can apply its own chat-template policy. vLLM token counts use the same
history and template settings. Failed or canceled responses never enter history.

All advertised tools use the same closed `{"input":{...}}` argument envelope
and strict schemas. Every declared field is required;
nullable fields use explicit null rather than omission. The provider sends
function.strict for every tool, including dynamically registered tools. There are
no selective strictness flags. Validate the full toolset against the serving
backend before deploying a schema or backend change. See [the input contract](docs/tool-input-contract.md).
The same immutable provider settings apply to the root, implementors, and auditors.

The vLLM adapter targets the generation fields exposed by vLLM 0.25.0, including
`chat_template_kwargs.enable_thinking`, Qwen's `chat_template_kwargs.reasoning_effort`,
and Nemotron's `chat_template_kwargs.force_nonempty_content`. These require support in the served
model's template. Settings are sent on every request; unsupported settings and
output/context limits remain visible server errors, with no automatic retry or
parameter substitution. Reasoning-history preservation is not implemented.

Shell and file tools run in the current directory, or the directory selected with
`-C /path/to/project`. The CLI enables them for the root and implementors. Auditors have a separate
prompt and omit the write/edit-file tools; shell access still has host permissions.
The CLI also provides `read_pdf`; see the PDF example below for its image-model
and Poppler requirements.
`web_search` and `open_url` are enabled for all four agent roles; see
[Web research](#web-research) for their browser dependencies. Use `-web=false`
to omit them. Missing backends produce a tool error when called; startup does not
launch a browser.
Commands run with host permissions, without a sandbox or approval prompt.

Type a message and press Enter. Input stays available while agents work and always
addresses Strap's root, regardless of which agent you are watching. The root's
live stream opens by default. At 40 columns by 18 rows or larger, compact
agent icons occupy a strip above the full-width conversation and composer.
The chips form the entire header, followed by a separator and the conversation.
The header starts at two rows and wraps when the terminal has room to show the team.
Each agent has a static glider icon, a task label, and a status symbol.
The icon configuration and accent color stay tied to the agent across chips,
message dots, and tool calls; previews do not animate. The selected chip and
working indicator use stronger accents, and the send button lights up for a draft.
Agents are grouped by attention needed, working, idle, inactive, and completed,
in discovery order within each group, without separate group headings. Root has
its own status chip beside All activity. Completed work is
expanded by default; idle agents with unfinished work remain visible.
Work awaiting review or blocked work remains distinct from an agent being idle;
`!` also flags execution errors. Agent previews show role, execution state,
parent, and the last context measurement; the root preview includes the model
and endpoint. Current activity and error/blocker details remain available there.
Unknown counts remain explicit.

Hover over a chip to preview its full task, agent ID, role, status, latest update,
and unread count. Press F6 and use the arrows or Tab / Shift-Tab to preview agents
from the keyboard. Previewing does not switch streams or mark their updates read.
Enter or a click opens the stream and returns to the composer; Escape or F6
returns without switching. When the team cannot fit in the available rows, the
strip scrolls horizontally to keep the focused chip visible; its edge arrows
and mouse wheel also navigate previews.
In smaller terminals, F6 opens a compact list in place of the transcript.
Select the Completed heading and press Enter, or press `c` anywhere in the
focused stacks or list, to expand or collapse it.
Clicking its disclosure does the same. `/focus [id]` selects
a live stream, `/focus root` returns to root, and `/focus all` shows All activity.
Viewing a stream never pauses an agent, changes its context, or redirects input.

Plans stay in a dock above the composer while the conversation scrolls. The dock
shows the current plan, completed-step count, and pending, working, review,
blocked, completed, or cancelled steps. Agent icons identify assigned work;
completion reflects accepted workflow state, not an agent saying it is done.
Click the plan header or press Ctrl+P to collapse it. Click a step for its latest
note, or press F8 and use Up/Down and Enter; Escape returns to the composer.
The wheel over the dock browses steps in long plans. Use `[` / `]` while the
plan is focused, the Next plan control, or `/plan [plan-id]` to select another
plan. Completed plans remain available, including after `/clear`; this is view
state, not a new persistence or execution mechanism. Short terminals reduce the
dock to a summary so the composer and conversation remain usable.
`strap eval` uses the same dock per problem, with Ctrl+P (or `p`) and F8 controls.

Each stream remembers its own scroll position. Output arriving above the text you
are reading preserves the current message anchor. Unread counts track changed
message/tool rows, so a streaming paragraph counts once rather than once per token.
Ctrl-End returns to live output and marks the selected stream read. Root's stream
includes messages routed to or from root; a child's unaddressed live output and
tools stay in that child's stream and All activity. `/transcript [id]` remains
the separate model-history inspector.

Prompts appear in shaded blocks; assistant replies and progress appear as plain
prose without repeated speaker headings. Commands show what actually ran, with a
static agent icon and two rows of output. Hover or click the icon for identity
and status. Ctrl+T expands or collapses tool output across the view. Click a tool's
status marker or disclosure hint, or press F7 and use Up / Down and Enter, to
expand an individual result. Escape returns to composing. Expanded details include
arguments and context-token measurements. Shell and file results show readable
output instead of JSON envelopes; failures and truncation remain visible even
when folded. Large payloads are capped in this view; `/transcript [id]` provides
the full model history. Each command stays in its original position between
progress messages, including when agents interleave. `/activity agent-id/response-number`
toggles only that response's tool results. Routine delivery receipts are tracked
internally rather than printed as messages.

The `strap eval` activity pane uses this same renderer, output folding, static
agent icons, and hover details. Its navigation remains read only.

Conversation messages render Markdown with headings, emphasis, lists, links,
tables, and code blocks using [Glamour](https://github.com/charmbracelet/glamour).
A dedicated light/dark theme gives all six heading levels clear hierarchy without
literal hash prefixes, frames syntax-highlighted code blocks, and styles nested
lists, task lists, quotes, links, strikethrough, dividers, and definition lists.
Formatting adapts to terminal width and color support, including `NO_COLOR`.
Source messages remain unchanged; rendering is cached until the width changes.

After an exchange, `Idle` means the agent is waiting for another message.
`queued` counts pending messages; it is a delivery status, not an agent state.

Command status updates while tools run; the agent strip shows which agents are
working or need attention. Assistant text accompanying tool calls appears as a
progress paragraph before that batch's tool rows. Agents are prompted to explain
their first action and meaningful findings between batches. These host-only
updates do not enter agent inboxes or mark work complete; the original text stays
in the generating agent's assistant history without an additional message.
Replies and progress text stream into a single row as the model generates them.
Thinking is omitted from the live view, including when tool output is expanded.
Ctrl+T controls tool output only. Cmd+T requires terminal-level forwarding as Ctrl+T
(`0x14`), where supported. The terminal normally reserves Cmd+T for a new tab,
and Strap's input library does not receive Command modifiers directly.
Reasoning is recorded for inspection and recovery. Successful responses retain
reasoning in assistant history for subsequent model requests, separately from
answer content. In `/transcript [id]`,
press `t` to switch between model history and reasoning inspection, `r` to refresh,
and scroll above the top for older calls. Inspection includes active and failed
calls and remains available after `/clear`.
Failed partial output remains visible. A reattached view can replay the entire
session and recover active output; see [streaming and recovery](harness/RECOVERY.md).

After a tool batch finishes, its expanded details show the agent's context size,
for example `12,345 context tokens`. The TUI counts that exact
history snapshot through the provider, including system instructions, tool
definitions, messages, and the completed tool results. Each tool row keeps its own batch's count, not a sum or the size of tool output alone.
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

Type `@` to browse files and folders, then type a path to filter the list.
Up / Down selects; Tab completes a file or opens a folder. Enter completes a
partial reference; Enter on an exact path sends the draft. Use `@src/main.go`,
`@src/`, or `@"notes/design draft.md"`. Relative paths resolve from Strap's working
directory (including `-C`); absolute paths also work. Mentions must start at a word boundary.
Trailing sentence punctuation is excluded; quote paths containing such punctuation.
Emails, escaped `\@` text, and mentions inside Markdown code spans/fences stay literal.

Attachments support UTF-8 text and code, including extensionless files. Binary
content and PDF, Word, spreadsheet, and presentation formats are unsupported.
Each file is limited to 256 KiB; the combined contents and the formatted attachment
section are each limited to 1 MiB, with at most 256 files and 10,000 visited folder
entries. These are byte limits, not a guarantee that a model's context window
will fit the message. Files are never silently truncated.

Folders expand recursively in sorted order. Inside repositories, Git evaluates
ignore rules (Git must be installed); outside repositories no ignore rules apply.
Git metadata, symbolic links, ignored files, unsupported formats, and oversized
files are skipped during folder expansion, with a summary of exclusions. Explicit
references to those files fail with an explanation. Empty selections and combined
budget overflows keep the draft unsent so you can narrow the references.

File discovery and attachment loading run asynchronously. Escape cancels loading;
editing the draft during loading prevents sending the old draft. Contents are
appended in a separate section of the model's message. The composer history keeps
your original prompt, so replaying it reloads current file contents without
expanding old attachments again.

Enter sends the entire draft. Alt+Enter or Ctrl+J inserts a newline; multiline
paste stays in the draft until you send it. The input grows up to six visible
rows and scrolls to keep the cursor visible. Up / Down moves within multiline
or wrapped drafts; Alt+Up / Alt+Down recalls message history and restores your
unfinished draft. Single-line drafts also retain Up / Down history navigation.
Tab inserts four spaces outside command and file completion. Slash commands run only from
a single-line draft, so pasted multiline text beginning with `/` is sent as a
message. Leading indentation and trailing newlines are preserved when sending.

The shaded composer always sends to Strap (root), regardless of the stream being
viewed. It grows with the draft, has a clickable send arrow, and shows brief
shortcut hints while typing. Operational status stays with the activity above.
Type `/` to reveal commands; there is no permanent command toolbar.

| Key or command | Action |
|---|---|
| Enter | Send the draft, or complete / run a slash command |
| Alt+Enter / Ctrl+J | Insert a newline |
| Up / Down | Select a suggestion, move within multiline input, or recall single-line history |
| Alt+Up / Alt+Down | Recall history / restore the unfinished draft |
| Tab / Escape | Complete / dismiss suggestions; Tab otherwise inserts four spaces |
| Escape (live conversation) | Cancel in-flight work through `/stop`, keeping the conversation and draft; open menus or previews dismiss first |
| F6 | Focus the agent stacks (compact list on small terminals) / return to the composer |
| F7 | Focus activity folds / return to the root composer |
| Up / Down, then Enter (folds focused) | Select and expand a group or tool result |
| Click a disclosure triangle | Expand / collapse an activity group or tool result |
| Click ↑ in the composer | Send the draft through the same path as Enter |
| Arrows or Tab / Shift-Tab, then Enter (agents focused) | Preview an agent, then open its stream and return to composing |
| Hover an agent chip / click it | Preview status / open its stream |
| `/focus [id\|root\|all]` | Watch an agent's live stream or All activity; defaults to root |
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
| `/stop` | Interrupt current work across all agents; keep the conversation |
| `/terminate [id]` | Permanently stop an agent; defaults to root |
| `/clear` | Clear the display, keeping conversation context |
| `/help` | Show commands |
| `/quit`, Ctrl-C, Ctrl-D | Cancel the conversation and exit |

`/stop` cancels active model/tool calls and outstanding delegated work. Completed
actions and conversation history remain; queued messages are marked undelivered.
Once interruption settles, send a new instruction to continue in the same session.
It does not undo edits or retry interrupted tools. `/resume` only resumes a pause;
use `/terminate [id]` when an agent should permanently exit.

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

## Evaluation

Run one coding problem per container with a consistent `/workspace`. The agent
publishes a snapshot to `/outbox`; a separate grader container runs hidden tests
against that snapshot and writes grades beside the traces in `/results`.

```sh
scripts/eval.sh easy-01-budget-pair
scripts/eval.sh --no-build --tier easy -- -profile qwen3.6
```

See the [eval guide](eval/README.md) for the agent and grader mount commands,
model configuration, quiet mode, and container smoke tests.

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

The opt-in discovery evaluation covers natural-language creation, reuse, audit, repair,
replacement, and worker escalation. See [the implementation and validation notes](docs/architecture/EXPLICIT_AGENT_WORK_DESIGN.md).

The CLI exposes tools according to each agent’s role:

| Tool | Contract |
|---|---|
| `create_plan` | Root creates the shared plan once, with nested initial steps |
| `add_step` / `edit_step` / `cancel_steps` / `reorder_steps` / `rename_plan` | Root changes one plan or one step per call using the plan revision; step status is never set here |
| `report_work_progress` | Current worker reports a full position, findings, or eligible scoped steps using an explicit work ID |
| `create_agent` | Root creates an idle registered `researcher`, `implementor`, or `auditor`; no task starts |
| `assign_implementation` / `assign_research` | Assign a bounded task to an existing eligible `assignee`; only implementation accepts optional plan scope |
| `assign_audit` / `assign_repair` | Assign review using the original work/revision/submission, or repair using the original work/revision/failed audit |
| `list_work` | Root discovers work in all states; optional assignee/kind/state filters, default 20 results, max 100, fixed-prefix continuation cursor |
| `get_plan` / `get_work` | Read current authorized snapshots, scoped steps, and available submission/repair findings |
| `get_audit` | Read an immutable verdict, summary, and findings using the event's `audit_id` |
| `submit_work` | Implementor or repair actor submits an outcome for review |
| `submit_audit` | Auditor records immutable pass/fail; failure requests changes and waits for explicit repair assignment |
| `reassign_work` / `cancel_work` | Owner recovery; reassignment requires an existing eligible assignee and never creates or stops agents |

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
stream is available through independent `session.Subscribe(ctx, harness.SubscribeOptions{After: cursor})` readers and
finite `session.Events(ctx, query)` pages, as described in
[the harness design](HARNESS_DESIGN.md). `Close(ctx)` rejects new commands,
cancels execution, drains final events, then closes owned resources. Its context
limits the caller's wait, not cleanup. A cleanup error preserves `Closing` state;
call `Close` again to retry unfinished resources. Inspection remains available.
Automatic token counting belongs to the session (`cfg.Telemetry`), so attaching
or detaching a view does not change provider traffic. Set `ContextTokens = false`
to disable it. `Configuration()` shows resolved role configuration and marks
injected providers as opaque. `cfg.ReasoningLimit` (CLI `-reasoning-limit`,
default 192 KB) cancels a model call that streams more reasoning than that
without acting and retries it once with a notice; a second overrun ends the
agent. Zero disables the limit. `Inspect()` includes capture coverage and health.

For evals, choose a completion rule explicitly: a root reply, idle agent, consumed
receipt, accepted work, and closed session are different facts. Grade the domain
result, call `Close` to finalize evidence, inspect capture health and
the accepted head, then `Dispose` after reading. Current capture includes
domain events, model history, streamed output and tool diagnostics; it does not
include raw provider protocol traffic or external artifact files. Compare causal events rather than assuming
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
reports capture failures separately from execution state. Memory retention keeps
the full session; configured quotas fail recording instead of evicting history. Lifecycle state
revisions are separate from model history revisions. Final inspection preserves
the shutdown reason, execution errors, and cleanup outcome after state advances
to `disposed`. Resolved configuration is included in recorded session events.

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

Tool constructors take application callbacks; `tool.CreatePlan` and
`tool.ReportWorkProgress` take plan-edit and work-progress handlers respectively. `work.Store` owns validation,
revisions, scope reservations, and immutable outcomes. It never creates agents or
sends messages. The application's dispatcher handles its pending events and
configures auditors independently from implementors.

See [the CLI wiring](cmd/strap/main.go), [the application dispatcher](internal/workflow/session.go),
and [the work contract](WORK_DESIGN.md). The dispatcher consumes the controller's
accepted session log for delivery observations, so work continues without a UI reader.
`conversation.Deliver` is a trusted host operation; model-facing messaging still
uses the runtime-bound `Sender`.

Tracked delegation uses `create_agent({input:{role:"implementor"}})`, followed by
`assign_implementation({input:{assignee:agent_id,task:"...",context:null,expected_output:null,scope:null}})`. Choose `auditor`
for independent review; implementors also handle repairs. Roles are immutable.
`harness.Session.CreateAgent(ctx, actor, roster.CreateRequest)` uses the same path.
The controller's raw `CreateAgent(parent, spec)` remains a work-independent runtime
primitive; it does not register application eligibility. The old task-only tool
callback and HTTP profile resolver have been removed.

`list_agents` and `inspect_agent` show role, registration, eligible work kinds,
and currently active work IDs. Use `list_work` to discover submitted, accepted,
closed, or cancelled work, then `get_work` for current mutation revisions. An empty
snapshot is not proof that an uncertain assignment failed. Work listing and agent
creation do not provide retry deduplication.

Creation and reassignment are not transparently retried. Work revisions prevent
stale writes; exact submission identity prevents stale audits. Owner notification
events remain pending until consumed; definite delivery failures are reported for
explicit recovery. There is no persistence or exactly-once delivery promise.
Revisions fence ledger mutations, not shell execution or shared filesystem writes.
Artifact references identify outputs to verify; resolving or isolating them belongs
to application tooling.

## Agent management

Hosts use `session.Interrupt(ctx)` for the user-facing Stop action. It waits for
execution, tool history, pending deliveries, and the work ledger to settle, then
holds agents in the nonterminal `interrupted` state. A subsequent `Send` releases
the hold. Reads remain available, but new work mutations are refused until fresh
input. A wait timeout leaves interruption running; another call joins the same
attempt. A dependency that ignores cancellation can delay completion. `StopAgent`
still permanently terminates an individual agent, and `Close` shuts down the
session. See [the interruption contract](docs/architecture/ADR-003-session-interruption.md).

The application connects ordinary tools to controller operations; `tool` imports
neither `conversation` nor `agent`. `StopAgent`, `PauseAgent`, and `ResumeAgent` accept a callback with this signature:

```go
func(context.Context, tool.Call, message.ActorID) (tool.Result, error)
```

`InspectAgent` accepts
`func(context.Context, tool.Call, tool.InspectAgentArgs) (tool.Result, error)`.
Its arguments are `agent_id`, nullable `limit` (1–100, default 20), and nullable
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

Multi-source investigations are enabled by default with `go run ./cmd/strap`.
The root assigns a researcher, which calls `deep_research` to plan, search, read,
verify claims and retain a run. The root remains available during the run and
reads the researcher's delivered brief; only the researcher reads runs, through
`get_research_run` bounded run/source pages. Cancellation and reassignment
retain a partial run under the original assignment. This uses
the web dependencies below. Disable it with `-deep-research=false`; `-web=false`
also disables deep research. See
[deep research configuration and evaluation](docs/architecture/DEEP_RESEARCH_IMPLEMENTATION.md).

`web_search` queries the [Tavily](https://tavily.com) search API and returns
ranked titles, destination URLs and snippets. `open_url` uses **agent-browser**
to load a page in its own headless Chrome session and read the rendered DOM,
returning readable text and a separate list of link destinations. Both appear as
ordinary tool activity and agent commentary in the console.

Set the key in the environment; it is never written to a config file, logged, or
quoted in an error:

```sh
export TAVILY_API_KEY=tvly-...
```

Search works on every platform this builds for. Scraping a public results page
does not: a search engine rate limits per address, so the first query in a quiet
window returns results and an immediate second one gets a challenge page. That
is why search is an API call rather than a browser driving a search engine.

The previous backend, **wkrender**, drives macOS WebKit and is still used when no
API key is set. It is macOS-only and subject to the same rate limiting, so treat
it as a fallback rather than the supported path:

```sh
make -C ../wkrender install   # macOS, Swift/Xcode required
```

Install agent-browser in an isolated directory:

```sh
npm install --prefix "$HOME/.local/share/strap/agent-browser" agent-browser
```

Strap discovers the packaged native executable there if `agent-browser` is not
on PATH. Strap does not require an exact agent-browser version; it validates
command responses when reading pages. The native browser runtime does not need Node. On macOS, Strap uses the installed Google Chrome executable
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

## Language servers (experimental)

Semantic code traversal is enabled by default for Go, Rust, Python,
JavaScript/TypeScript, and Bash. Install the corresponding language servers on
`PATH`, then run Strap normally:

```sh
go run ./cmd/strap -C /path/to/project
```

The seven tools provide status, symbol search, file outlines, inspection,
definition/type/implementation navigation, references, and diagnostics. All roles
share one session-owned manager and receive instructions for semantic search,
navigation, and checking edits. Servers start lazily; missing executables do not
prevent startup. `-lsp-config <file>` replaces the presets with a complete server
configuration. `-lsp=false` disables the feature, including in eval sessions.

See [setup, tool contracts, extension API, and freshness limits](docs/lsp.md).
Interoperability fixtures cover all six languages. [Live Qwen3.6 checks](docs/evals/2026-09-20-lsp-reliability.md)
exercise tool selection, targeting, reference reuse and recovery costs. Source
targets use exact identifiers; Strap calculates character positions. Rename,
code actions, and call hierarchy are not included.

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
| `shell` | `command`, nullable `timeout_ms` | Combined output, exit code, timeout and truncation flags |
| `read_file` | `path`, nullable `offset`, `limit` | Numbered text, total lines, more/truncation flags |
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

[Streaming and recoverable session views](harness/RECOVERY.md) are implemented:
one retained session log, subscriptions that replay then follow, and passive views
reconstructed from accepted records.

The public session API and adapter-independent architecture are tracked in
[the audited harness design](HARNESS_DESIGN.md). Shared typed workflow operations
back both the model tools and the public `harness.Session` API. Session assembly, coordinated shutdown, bounded publication, JSONL recording,
independent observation, automatic telemetry, and the HTTP adapter are implemented.

Agent messages, receipts, work state, and model history remain in memory. Event
persistence does not restore running execution. Mutation deduplication, model
context compaction and broader runtime memory budgets
are deferred. There is no behavior framework, permission stack, workspace
model, or configurable workflow engine. Work revisions validate ledger mutations;
they do not gate general model responses or local-tool execution.

The root's delegation role is expressed through its instructions and supplied
tools. Publication and storage read buffers are bounded; agent histories and
outstanding domain work remain in memory. Models and tools must
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

Worker progress uses `report_work_progress` with explicit `work_id`.
The obsolete progress-mutation Go API and HTTP route are removed. A supplied position replaces all its fields;
set it to null in tool calls for finding-only or step-only reports. HTTP uses `POST /sessions/{id}/work/report-progress` with
`{"actor":"agent-id","request":{"input":{...}}}`. Model-command HTTP adapters
share tool validation and domain conversion. Host controls and stored results
keep their own formats.


## Research and recorded progress

`create_agent` supports `researcher`, `implementor`, and `auditor`. Creation is
idle; the appropriate `assign_implementation`, `assign_research`, `assign_audit`,
or `assign_repair` tool starts an assignment. Research takes a bounded question,
context and expected output without plan scope. `submit_research` stores an
immutable brief and moves research to `delivered`. Implementation and repair
still use `submit_work` and require independent audit for acceptance.

| Surface | Root | Researcher | Implementor | Auditor |
| --- | --- | --- | --- | --- |
| `get_work`, `get_plan`, `get_audit`, `get_work_progress`, `get_research_brief` | Yes | Yes | Yes | Yes |
| `report_work_progress` | — | Yes | Yes | Yes |
| `update_plan`, create/assign/reassign/cancel work | Yes | — | — | — |
| `submit_research` / `submit_work` / `submit_audit` | — | Research | Work | Audit |
| `wait_for_input` | Yes | Yes | — | — |
| Local shell | Standard | Assignment-bound diagnostic | Standard | Standard |
| File editing | Yes | — | Yes | — |
| Messaging and configured file/PDF/web reads | Yes | Yes | Yes | Yes |

Worker progress requires explicit `work_id`; its assignment binding is read from
the work. Other work mutations also require the current `expected_revision`. A supplied `position` replaces the entire prior position;
setting it to null in tool calls preserves that position. Findings are immutable, with observed or
inferred basis, evidence, limitations and explicit supersession. Step progress
updates the authoritative scoped plan immediately. The removed worker `update_work` tool is unavailable.

The dispatcher records every report. Activity-only reports stay in inspection;
finding notices batch for two seconds and normally occur at most once per owner
per fifteen seconds. Changed blockers/decisions and delivery are immediate.
Notices carry bounded references, not report bodies. Submission, cancellation and
reassignment retire obsolete report wakeups. Queued messages remain in history;
recorded admission decisions suppress stale-only new exchanges while preserving
ordinary tool continuations. Runtime waiting and reported blockers remain distinct.

Every new exchange begins with a harness-computed state block, appended to the
agent's history after the queued inputs and before the first model call. The
root sees its plans with live steps, statuses and reservations, plus its owned
work with assignees and latest submission, audit and brief ids; a worker sees its
assignments with work and assignment revisions and scoped step statuses. The
block is read from the accepted log at wake time, so it is never stale, and the
model cannot write a guessed id into it. Agents with nothing owned or assigned
receive no block.

Research diagnostics use a separate shell configured by `ResearchExecution`
(default 30 seconds, maximum 60 seconds, 16 KiB retained output). Every command
requires an explicit work/assignment binding; a later reassignment cannot relabel
its evidence. These are execution bounds, not a read-only sandbox. Host-issued
`evidence_ref` receipts follow accepted finish records, including failed/cancelled
outcomes and partial captures. Findings may cite prior-assignment evidence from
the same work, preserving attribution; unknown and cross-work execution references
are rejected. Large model receipts direct the reader to the complete retained
capture; bytes discarded by the shell are not recoverable.

`get_work_progress` reads current progress, reports, findings or execution evidence.
`get_research_brief` reads brief records. Both use bounded fixed-prefix pages and
signed cursors, with current live authorization checked on every continuation.
The live HTTP equivalents are `/sessions/{id}/progress-view` and `brief-view`.
Passive archive readers reconstruct the same records without starting agents.

`wait_for_input` must be the sole call in a batch. It settles the tool history
and waits without a final reply or extra generation. A root answer to a waiting
researcher must be sent explicitly to that researcher through `send_message`.

Implementation checkpoints and validation are tracked in
[RESEARCH_IMPLEMENTATION.md](docs/architecture/RESEARCH_IMPLEMENTATION.md).

## License

[MIT](LICENSE). Keep the copyright notice with copies and substantial portions.
