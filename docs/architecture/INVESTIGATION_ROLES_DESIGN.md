# Investigation roles: web researcher, deep researcher, experimenter

Status: design agreed 2026-09-25. Phase 1 (the researcher split) and phase 2
(isolated copies, the auditor switch and the experimenter) are built in that
order; each section below records what shipped.

Update 2026-09-27: the per-kind assignment tools became one `assign_task`
whose `kind` names the work and its role. The reviewer's work is its own kind,
`review`, and web and deep researchers take `web_research` and
`deep_research`; all three deliver a brief with `submit_brief` (was
`submit_research`), read with `get_brief`. Tool names below are as shipped
then.

## Problem

Any question that needed the web ended in deep research. The path:

1. The manager has no web tools. Its prompt sends anything that needs external
   sources to a researcher.
2. `DefaultConfig` enables deep research, so every researcher held
   `deep_research`.
3. With it enabled, the harness appended "for a multi-source investigation,
   use deep_research" to the researcher's prompt, and told the manager that
   researchers have it. Almost any web question reads as multi-source, so the
   researcher started the minutes-long engine instead of searching.

Nothing structural tied the engine to a request for it. The user's rule is that
deep research runs only when they explicitly ask for it.

Tracing it exposed a second problem. The researcher also held a research
diagnostic shell ("bounded discovery and build or test diagnostics… may execute
project code and create generated files"), and the manager was told to use
researchers for "behavior that needs an experiment". That shell writes as
easily as `write_file` and verifies like an auditor, so the researcher held
three powers: the web, the deep engine, and execution. It predates the
[segmented capabilities](../../README.md) decision of 2026-09-23 and was never
revisited.

## Principle

A role holds one power, and the power is what its tools can do or cost: reach
the network, spend minutes of model time, run commands, write the workspace.
Subjects are not powers. Every role reads the workspace, so a "code researcher"
or "file-system researcher" would hold nothing the others lack, and each extra
role is one more routing decision for the manager, which is where small models
fail most.

Audits and experiments both run commands but ask different questions:

- An **audit** is about correctness. It takes an existing claim, a submission
  or a proposal, and says whether it agrees.
- An **experiment** is about existence. It takes a problem, forms hypotheses,
  builds a method to measure them, and reports what is there, including that
  nothing is.

## Roles

| Role | Power | Tools beyond reads, messaging and progress | Delivers |
|---|---|---|---|
| manager | plans and assigns | coordination, agent control | replies to the user |
| implementor | writes the workspace | `write_file`, `edit_file`, `shell` while holding work | a submission |
| auditor | judges a claim | `shell` in an isolated copy (phase 2) | a verdict |
| web_researcher | reaches the web | `web_search`, `open_url` | a research brief |
| deep_researcher | runs the deep research engine | `deep_research`, `get_research_run` | a research brief |
| experimenter | measures behavior | `shell`, `write_file`, `edit_file` in an isolated copy, experiment records (phase 2) | an experiment report |

Routing, one line each, as the manager's prompt states it:

- external facts, libraries, documentation → `web_researcher`
- the user explicitly asks for deep research → `deep_researcher`
- how the code actually behaves: measurement, reproduction, sweeps → `experimenter`
- change the code → `implementor`
- check a submission → `auditor`

`deep_research` does not spawn web researchers. The engine is already a fixed
pipeline: it plans sub-questions, runs parallel scouts through the web adapter
(`research/scout.go`), and verifies citations in a separate pass, all under hard
limits. Spawning agents would hand that orchestration back to a small model and
break the tree, where only the manager creates workers. The deep researcher's
own job stays thin: frame the question and success criteria, launch the engine,
read its claims, record the ones it delivers, submit.

## Phase 1: the researcher split

- `roster.Researcher` becomes `roster.WebResearcher` (`web_researcher`), and
  `roster.DeepResearcher` (`deep_researcher`) is added. Both are workers under
  the manager with the researcher's edges, and both accept research work.
  The name `researcher` no longer exists; traces that use it stop loading, as
  traces with a root did.
- `create_agent` offers both roles.
- Only the deep researcher holds `deep_research` and `get_research_run`, and
  it holds no `web_search` or `open_url`: in the first live runs a deep
  researcher with both reached for `web_search` first. It reaches the web only
  through the engine, whose runs carry the source text. When
  deep research is disabled (no web, or `--deep-research=false`),
  `create_agent` refuses the role and names `web_researcher` instead, and the
  manager's prompt does not mention it. The web researcher's prompt carries no
  deep research instruction.
- The research diagnostic shell is removed: the tool, `WithResearchDiagnostic`,
  `ResearchExecutionConfig`, its default, and the prompt line. Researchers
  read, and nothing else touches the workspace. Execution evidence refs are
  still issued by the implementor and auditor shells (`work_execution.go`).
- The manager prompt drops "behavior that needs an experiment" and states the
  deep research rule: create a deep researcher only when the user explicitly
  asks for deep research.
- The state machine gains a `research` intent for user messages that ask for
  outside information: the manager's report on one must follow a research
  assignment, as a report on a task must follow an implementation.
- Machine rules (`harness/machine/rules.go`):
  - web tools are used only by the two researcher roles;
  - `deep_research` is called only by a deep researcher;
  - a deep researcher is created only after a user message that asks for deep
    research. The check is lexical ("deep research", "deep-research",
    "deep dive"), which is enough to flag the failure this design exists for.

Until phase 2 lands, "how does this behave?" has no role of its own; the
manager's prompt no longer names experiments as research.

### Validation (2026-09-25, qwen3.6)

Fresh live sessions recorded with `strap` as built, then the manager's first
call probed 20–40 times with `strap eval probe` (its classifier now names the
role passed to `create_agent`):

| Prompt | Manager's first call |
|---|---|
| explicit deep research (kitty keyboard protocol) | `deep_researcher` 23/30, never `web_researcher` without the no-recall line and 1/30 with it; the rest read or plan first |
| explicit deep research (vLLM speculative decoding) | `deep_researcher` 17/20, the rest `list_agents` |
| plain web questions (Go release, SQLite, Bubble Tea) | never `deep_researcher` in 60 samples |

In the live runs the deep researcher called `deep_research` first and the web
researcher `web_search`. `strap eval replay -verify-only` finds no topology
violation in any trace.

The probes exposed a different failure: the manager answered plain web
questions from memory (SQLite 19/20, Go 3/20, Bubble Tea 3/20 samples), with a
different release date in each live answer. The manager prompt now closes with
"You have no web tools, and what you remember about software, libraries,
versions, APIs and current events may be out of date…". On SQLite it cuts
answers from memory to 12–13 of 30–40 samples; neither moving it nor
reordering it against the deep research sentence changed that. It is a prompt
rule, so the `research` intent in `strap eval replay -intents` flags the rest.

## Phase 2: isolated copies, the auditor switch, the experimenter

### Isolated copies

The auditor was told to `cp -r /workspace /tmp/audit` before writing tests, a
prompt rule. Now the harness gives each audit and experiment its own copy of
the workspace (`harness/isolation.go`):

- The copy is made the first time the assignment's tools run, from the
  workspace as it is, including uncommitted changes. On macOS it uses APFS
  clones (`cp -c`), on Linux reflinks where available, else a plain copy.
- The auditor's and experimenter's shell, file and PDF tools act on the copy.
  The copy is transparent: paths naming the workspace are rewritten to the
  copy on the way in and back on the way out, so the model uses the workspace
  paths it sees everywhere else and never learns another. Language tools keep
  reading the workspace.
- The shell cannot be confined without a sandbox. A command that reaches the
  workspace by a path the rewrite misses is caught by the workspace snapshot
  the environment recorder already takes, and the machine rule "only
  implementors change the workspace" flags it.
- A copy is removed once its assignment is no longer active, and every copy
  when the session closes.

The auditor's prompt says its tools act on its own copy, so it writes tests
beside the code.

### The experimenter

The experimenter is handed a problem ("look for performance issues in TUI
rendering"). It reads the code, forms hypotheses, builds a measurement harness
in its copy, runs it, and reports what it found, including that nothing is
wrong.

The scientific method is enforced by the ledger (`work/experiment.go`), not
the prompt:

| Tool | Records |
|---|---|
| `record_hypothesis {work_id, statement, prediction, method}` | A pre-registered hypothesis: the claim, the observation that would confirm or refute it, and how it will be measured. It is stored on the experiment's work item, so `get_work` lists them. Returns a `hypothesis_id`. |
| `run_trials {command, trials, timeout_ms}` | Runs a command up to 20 times in the copy and returns each trial's wall-clock time and exit code, the median, minimum and maximum, and the last output, under one evidence ref. |
| `record_result {work_id, hypothesis_id, verdict, observed, evidence_refs}` | `supported`, `refuted` or `inconclusive`, once per hypothesis. Supported and refuted cite at least one run; every cited run must be bound to this assignment and issued after the hypothesis (the store orders runs and hypotheses on one sequence). |
| `submit_experiment {work_id, expected_revision, summary, method {reproduce_command, files}, recommendation, proposed_steps}` | Delivers a `conclusion-…`. Refused without hypotheses or while any lacks a result. The method files are read from the copy, up to 16 files of 32 KiB, 64 KiB in all. |

Pre-registration is the point: the prediction is fixed before any measurement,
so the model cannot decide what success means after seeing the data. A refuted
or inconclusive hypothesis is a full result, and a conclusion whose hypotheses
are all refuted says the code is fine.

The manager assigns with `assign_experiment` (work kind `experiment`, which
only an experimenter accepts) and is notified when it is delivered. Any agent
reads a conclusion with `get_conclusion`, because its method is what an
implementor adds and an auditor reruns on the fix; the manager names the
`conclusion_id` in their assignments. Hypothesis events do not wake the
manager.

The experimenter has no web tools. If it needs documentation it asks the
manager, who assigns a web researcher. Without local tools there is nothing to
measure in, so `create_agent` refuses the role.

### Machine rules

The ordering rules are ledger refusals, so they cannot be broken. The machine
adds:

- an experiment moves new → active → delivered | cancelled;
- the `research` intent accepts a delivered experiment as well as research;
- an auditor's or experimenter's write that reaches the workspace breaks
  "only implementors change the workspace".

### Validation (2026-09-25, qwen3.6)

Live sessions on a small Go package whose `Unique` compares every name with
every kept one:

- **Audit in a copy.** "Add a Count function" went to an implementor, then an
  auditor, which wrote `count_verify_test.go`, ran `cd <workspace> && go test`
  (rewritten to its copy) and passed the submission. The workspace kept only
  its three original files.
- **Experiment, when asked for.** "Assign an experimenter to look for
  performance issues" ran the whole method: two hypotheses recorded before any
  run, a benchmark written in the copy, five `run_trials` calls from 100 to
  100K names, both results recorded with run evidence, and a conclusion with
  the benchmark file. Its data refuted the O(n²) hypothesis *for its input*:
  the generator drew from 20 names, so the kept list saturated. The
  conclusion says so with its numbers, but the experimenter never tested many
  distinct names, the case that matters. Experiment design has the same
  thoroughness gap audits had.
- **Routing does not reach the experimenter.** Asked "look for performance
  issues in dedupe.go" or "does Unique treat Straße and STRASSE as
  different?", the manager read the code and answered itself, in the second
  case with a wrong fix (NFKD does not map ß to ss). Probed at its answering
  call: 27/30 answers itself as built, 26/30 with an added "reading code shows
  what it says, not how it behaves" line. Without its file tools its first
  call orients with ledger reads and creates an experimenter in 1–5 of 30
  samples; a single-call probe cannot show where the turn ends. The manager
  keeping file reads was a deliberate choice, so changing it is open.

Replay verification of these traces finds no rule broken by the new roles
once the recorder distinguishes a write to a copy (the environment record now
notes that the workspace was compared).

## Phase 3: the reviewer (2026-09-25)

The manager answered questions about the workspace from its own file reads
instead of assigning them: 27 of 30 samples at its answering call, unchanged
by prompt wording. The user's decision: take the manager's file tools away and
give reading to a new role, so the manager cannot answer and always delegates.

- **`reviewer`** reads the workspace: read_file, glob, grep_search,
  list_directory, read_pdf and language tools, no shell or writes. It takes
  research work (`assign_research`) and delivers a brief whose findings cite
  files. It is named reviewer, not code reviewer, because the workspace is
  not always code ("organize my downloads folder").
- **Organizing is a change.** The reviewer surveys a folder and proposes the
  organization as proposed steps; an implementor moves the files. Moving files
  is a write, and only implementors write the workspace.
- **The manager has no file, shell or language tools**, no first-wake
  workspace listing, and no file or language instructions in its prompt. Its
  prompt routes workspace questions to a reviewer.
- **Web and deep researchers lost their file tools too**: `web_researcher` is
  `web_search` and `open_url`, and reading the workspace is the reviewer's
  power. Only the implementor, auditor, experimenter and reviewer touch files.

### Validation (2026-09-25, qwen3.6)

Fresh live sessions with the manager's file tools removed:

- "look for performance issues in dedupe.go" → experimenter, which measured
  and delivered (before: the manager read the code and answered itself);
- "what does Unique do with names that differ only in case?" → reviewer brief;
- "organize the files in this folder by type" → reviewer survey, implementor
  moved 14 files into type folders, auditor passed (after reading only the
  implementor's run evidence, not the folder itself).

Wording probe for answering from memory, the manager's first call, 20 samples
per cell. Answered from memory, of 60 across the SQLite, Go and Bubble Tea
questions: no instruction 18; the current no-recall sentence 0; plus today's
date 2; a knowledge-cutoff framing 3 (3 with the date); a when-to-search list
6 (4 with the date); never state specifics from memory 1 (0 with the date).
A stable question ("what does defer do in Go?") was answered directly in
18–20 of 20 under every variant (the date sent 2 to a web researcher); an
explicit deep research request went to a web researcher in at most 1 of 20.
The current sentence stays; date injection adds nothing measurable. With the
manager's file tools gone the same sentence went from about 30% answers from
memory to 0 of 20 on SQLite, though the live session behind that trace still
answered from memory once.

### Folders outside the workspace

"What files are in my downloads folder?" was first answered with a question,
then refused ("I can only inspect files within the current workspace"). The
tools had no such limit; every description the manager saw said the reviewer
reads "the workspace". Now:

- The reviewer reads files anywhere on this machine: the workspace or any
  folder the user names. The manager's prompt says a folder named by its usual
  name, such as ~/Downloads, is that folder, not a question to ask.
- File tools expand `~` and `~/…` to the home directory, as a shell does.
- Implementors may write outside the workspace (the user's decision); the
  workspace snapshot does not see those writes. Auditors may too (the user's
  decision, 2026-09-26): their write_file to /tmp was refused while their
  shell could write there, so they wrote scratch programs through shell
  heredocs instead. Experimenters read outside folders but their file writes
  there are refused, because only the workspace is copied.

Probed on the exact prompt, the manager created a reviewer in 30 of 30
samples (5 after first checking list_agents), never asking or refusing.

## Phase 4: investigation steps complete (2026-09-25)

Plan steps completed only when implementation scoped to them was accepted, and
research could not be scoped, so a "survey the workspace" step stayed pending
forever; in ladder medium-10 the manager cancelled it to tidy the plan. With a
reviewer phase in most plans that gap showed up everywhere.

Each kind of work now completes the steps it covers in its own success state:

| Work | Completes its scoped steps |
|---|---|
| implementation, repair | when an audit accepts it, from ready_for_review (unchanged) |
| research (reviewer, web, deep) | when delivered with `submit_research` |
| experiment | when delivered with `submit_experiment` |

- `assign_research` and `assign_experiment` take the same nullable `scope`
  as `assign_implementation`. The store reserves scoped steps exactly as it
  does for implementation (`checkScope`, `reserve`): a step belongs to one live
  item, and a reserved step cannot be edited or cancelled.
- Delivery completes the scoped steps and releases them in the same mutation
  (`settleScope`); cancellation releases them back to pending.
- Researchers and experimenters report no step statuses; delivery is the only
  transition, open → completed. The state machine allows it only when
  delivered research or an experiment covers the step, and still requires
  implementation steps to pass through ready_for_review.
- The user's decision: any delivery completes the step, like acceptance. An
  inconclusive experiment or a brief with open questions still did the
  investigation the step asked for; the manager adds a follow-up step if the
  answer is not enough.
- The manager's reply check now looks for uncovered open steps once it has
  assigned implementation or any scoped work.

## Non-goals

- A sandbox for the shell. Detection after the fact is what the harness has
  for implementors too; a sandbox would change every role.
- A parameter sweep tool. `run_trials` repeats one command; a sweep over
  parameters is added only if traces show experimenters looping by hand.
