# Role prompt review — 2026-09-14

The tic-tac-toe trace showed root implementing directly, never assigning work or
triggering review, and announcing completion after rejected plan-status updates.
The user reported that automatic delegation worked before the recent prompt
rewrite. This review restores explicit role ownership and workflow instructions;
it does not change tools, model settings or lifecycle enforcement.

## Review and decisions

Three independent agents reviewed the proposed root prompt for tool contracts,
behavioral failure modes, and consistency with the research/work design. They
reviewed the resulting diff again before the root checkpoint, `5dc0497`.

- Root explicitly owns conversation, planning, delegation and audit coordination.
  Requests beyond a direct conversational answer require a shared plan and
  delegated execution. Brief direct inspection supports coordination and evidence
  checks; implementation and substantial investigation belong to workers.
- Assignment instructions specify the actual fields for implementation, research,
  audit and repair. Audits follow submission without a user nudge. Current-state
  checks prevent duplicate audits and repairs; independent auditors must not have
  contributed implementation or repairs.
- Root distinguishes rejected mutations, progress, submission and acceptance.
  It waits for active work and reports implementation success after acceptance.
- Research progress uses the shared work-progress vocabulary. Delivered results
  retain the documented `get_research_brief` reader.
- Research-only plans expose an existing lifecycle gap: their steps cannot reach
  completed through research delivery or root synthesis. The root can answer the
  research request but must not claim those steps completed. See the role policy
  in [the design](RESEARCH_STATUS_DESIGN.md).

After root framing was finalized, each reviewer audited one worker role and its
shared instructions, then checked the implemented diff. No blocking findings
remained. Each worker now has an explicit identity, assigned authority, progress
obligations, evidence standards, submission procedure and handoff to its owner.

The review also made tool differences explicit: progress reports require the
assignment binding, implementation and audit submissions omit it, and research
submission requires it. Only scoped implementation/repair may report step
changes. Root and researcher can use `wait_for_input`; implementor and auditor
use ordinary inbox handoffs. Research diagnostics have their own shell selectors.

## Validation

- Root checkpoint: `go test ./harness ./tool ./work ./prompt` passed.
- Final worker prompts: `go test ./...` and `git diff --check` passed.
- Tests used a writable temporary Go cache and permission to bind localhost HTTP
  test ports; initial sandbox failures were environmental.
- A temporary live runner attempted a conversational control, an ordinary Go
  implementation request without delegation/audit instructions, and a research
  request. The first control failed before model output because
  `http://192.168.1.237:8364/v1/chat/completions` refused the connection. The runner
  was stopped; implementation and research trials did not run.

Static review and deterministic tests do not prove model adherence. A live
regression check remains necessary when the endpoint is available. It should
verify actual plan/delegation/submission/audit ordering, accepted scope before
completion, root commentary, and research delivery without code changes or an
implementation audit. Inspect trace commands and fixture contents as well as
lifecycle events; event counts alone do not establish these properties.

## Follow-up: explicit request routing

The user subsequently reported root performing an explicitly requested research
task itself. The opening instructions now define direct-response and agent-workflow
conditions by requested actions rather than terms such as "ordinary conversation"
or "a plan requires delegated execution."

Explicit research requests require researcher assignment. Root creates a phased
plan and assigns each phase before execution. Direct responses require no source
inspection, commands or changes; status replies reuse the existing workflow.
Root's preparation reads are limited to conversation and recorded work/results.
Additional source inspection is assigned to a researcher. The root-specific web
instruction now describes citing researcher evidence instead of telling root to
perform searches itself. Worker prompts and tool capabilities are unchanged.

This follow-up clarifies the policy; it is not evidence that the latest reported
interaction used these prompts or that a model now follows them.
