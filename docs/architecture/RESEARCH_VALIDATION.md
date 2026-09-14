# Research validation — 2026-09-13

Areas 1–14 are implemented in separate rollback checkpoints. Area 15 has a
passing deterministic acceptance gate and a runnable bounded live comparison;
its live-model gate remains open because neither saved model server was reachable.

## Deterministic evidence

- `go test -race ./...` passed, with remote evaluations explicitly disabled.
- `TestResearchEndToEndToolsAndCoveredTimer` exercises the registered tools from
  researcher creation through diagnostic execution, evidence-backed reporting,
  brief delivery, root brief/finding/evidence reads and final reply. It observes
  seven root calls and four researcher calls, and no obsolete timer exchange
  after the answer.
- Evidence integration reads exact retained diagnostic output beyond byte 4,096
  through 2,048-byte serialized pages, checks live revocation after reassignment,
  and resolves the same reference from a passive JSONL archive.
- Stopping a researcher retains the partial diagnostic capture and cancellation
  outcome in the finish record without another provider call or worker report.
- Domain and agent tests cover forged/cross-work evidence, inherited attribution,
  assignment A versus B, reassignment back to the same actor, failed record
  publication, silent stale-only admission, ordinary tool-error continuation,
  sole-call yielding, queued help, and mixed-control rejection before execution.
- Notification tests supply time explicitly to the queue to check batching,
  interval boundaries, immutable batches, serialized limits and retries. Coverage
  tests include secondary repair/audit work and retiring only the old assignment.
- TUI tests verify attributed findings and uncertainty, distinct research delivery,
  preserved chronology and visible commentary/errors during disclosure, with no
  messages or runtime controls emitted by viewing.

One full-suite attempt reported `operation not permitted` during process-group
cleanup in the timeout test. Five isolated race runs and the subsequent full race
suite passed. No error suppression was added: cleanup failure remains a recorded
outcome and returned error. The initial OS failure was not reproduced; process-group
cleanup remains best effort within its documented host boundary.

The CLI catalog already selected Nemotron in commit `6251d7e`. Commit `0087d84`
updates two stale tests and the README to match it; model selection was not changed.

## Live comparison gate

`TestResearchLivePlanComparison` runs two sequential trials on a temporary Go
fixture: direct root inspection and one delegated researcher. The task compares
PLAN.md requirements with implementation and test coverage. Each trial has a
three-minute deadline, 24-provider-call ceiling and 8,192-token response ceiling;
web tools are disabled. The test checks that source inputs remain unchanged.

The fixture omits negative-input validation and its corresponding tests. Grading
checks that the answer identifies the gap and discusses tests; a delegated answer
must follow an actual research delivery. Trace and JSON metrics retain the answer,
latency, root/total calls, rejected tools and delivery status for human review.
Those checks are narrow acceptance signals, not a broad model-quality benchmark.

Run against an available server:

```sh
STRAP_RESEARCH_EVAL_URL=http://host:port \
STRAP_RESEARCH_EVAL_MODEL=model-alias \
STRAP_RESEARCH_EVAL_OUTPUT=/private/tmp/strap-research-eval \
go test ./harness -run '^TestResearchLivePlanComparison$' -v -count=1
```

Availability checks on `192.168.1.237:8364` and `192.168.1.237:8355` both failed to
connect. No real-model comparison completed, so latency, extra root calls and live
summary fidelity remain unmeasured. A working endpoint/model alias is required to
close area 15. No production prompt changes were derived from these fixtures.
