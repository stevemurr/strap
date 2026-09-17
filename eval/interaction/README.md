# Focused interaction evaluations

This suite tests one coordination decision from a known state, using production
session operations and tool dispatch. It reports harness correctness separately
from actor behavior so an invalid request can be a harness success and an actor
mistake in the same trial.

The five audit-assignment scenarios are version 2 and use `assign_audit` with no
`kind` argument. Their version 1 results used the removed `assign_work` interface;
saved traces and analysis remain historical and are not silently migrated or
regraded. The eight schema regression scenarios below start at version 1.

The first pilot covers assigning an independent audit:

| Scenario | Controlled case |
| --- | --- |
| `audit-independent` | Assign an eligible auditor to the latest submission. |
| `audit-wrong-assignee` | Reject the implementation contributor as auditor, then assign correctly. |
| `audit-old-submission` | Reject a superseded submission, then use the latest submission. |
| `audit-stale-revision` | Reject an outdated work revision, then use current work. |
| `audit-revision-race` | Change the real revision while an otherwise valid assignment is in flight; recover from the resulting conflict. |

The first four scenarios retain the original pilot behavior: scripted negative
cases deliberately issue the invalid request, while live variants provide an
outdated or misleading handoff. The model can avoid the mistake entirely or
recover after rejection; rejection is not required for those live cases.
The revision-race case instead requires an actual conflict and successful recovery.

In `audit-revision-race`, the runner waits until the model proposes its first
otherwise valid audit assignment. Before dispatching that response, it uses
public host operations to assign and cancel a competing audit. The original
work returns to `needs_check` at revision +2 with its submission and plan intact.
The model's unchanged request then meets the production revision guard. Recovery
can use a fresh read or the authoritative current revision in the conflict
feedback; the evaluation does not demand a redundant read. The oracle verifies
the exact injected changes and permits only that cancelled audit in addition to
the one active audit the model must arrange. The result records intervention
cursors, the triggering request, and before/after work for inspection.

The required outcome is one correctly bound audit, original work in `checking`,
and unchanged plan completion. Rejected mutations must preserve domain state.
This pilot measures assignment legality and selection; it does not measure an
auditor's ability to judge code or establish sufficient verification evidence.
Deterministic tests also exercise the graders with deliberately faulty provider
responses and corrupted state transitions.

## Schema regressions

These scenarios target known contract failures after the assignment-tool split.
They run the production catalog and dispatcher for the relevant role: the root
assigns an audit, an auditor submits a verdict, and an implementor reports progress.

| Scenario | Malformed operation exercised by the script |
| --- | --- |
| `schema-audit-repair-field` | Send `audit_id` to `assign_audit` instead of its required `submission_id`. |
| `schema-audit-required` | Supply only the assignee, omitting required work, revision, and submission bindings. |
| `schema-audit-kind` | Include the removed `kind` selector on `assign_audit`. |
| `schema-audit-null-extra` | Supply an unknown field with a null value; it must not be silently discarded. |
| `schema-removed-assignment-tool` | Call the removed `assign_work` tool. |
| `schema-audit-fail-findings` | Submit a failed audit without the required findings. |
| `schema-audit-pass-findings` | Submit a passed audit with findings, which that verdict forbids. |
| `schema-progress-objective` | Put `objective` outside its required progress structure; it must not be silently relocated. |

Each script sends the specific malformed call, observes rejection, then sends a
corrected call. The oracle independently validates arguments against the actual
advertised JSON Schema, checks that the rejected operation left domain state
unchanged, and verifies the corrected operation's bound state transition. A
missing advertised tool counts as an invalid contract call.

Live variants present a task and potentially misleading handoff to one real model
actor. They measure whether the model chooses the right tool and valid arguments
on its first operation, or recovers if it makes a mistake. A live model may avoid
the malformed call entirely; such a pass does not exercise the rejection guard.
Verdict cases use small arithmetic submissions with controlled evidence. They
begin after a real purpose and verification report has been recorded, and test
encoding and recording the final verdict rather than general code-review
performance. The evaluated worker is paused before its assignment is seeded,
so its first model request includes the complete fixture and final stimulus.

## Run it

Scripted trials require no model catalog or network connection:

```sh
go run ./cmd/strap-eval interaction list
go run ./cmd/strap-eval interaction run -out /tmp/strap-interaction-scripted
go run ./cmd/strap-eval interaction report /tmp/strap-interaction-scripted
```

Use `-mode live -profile PROFILE` to evaluate one real model actor against
controlled collaborators. For example:

```sh
go run ./cmd/strap-eval interaction run -mode live -profile PROFILE \
  -scenario audit-independent,audit-stale-revision -repeat 5 \
  -out /tmp/strap-interaction-live
```

To isolate concurrent revision recovery against a live model:

```sh
go run ./cmd/strap-eval interaction run -mode live -profile PROFILE \
  -scenario audit-revision-race -repeat 5 -out /tmp/strap-interaction-race
```

To run only the eight schema regressions:

```sh
go run ./cmd/strap-eval interaction run \
  -scenario schema-audit-repair-field,schema-audit-required,schema-audit-kind,schema-audit-null-extra,schema-removed-assignment-tool,schema-audit-fail-findings,schema-audit-pass-findings,schema-progress-objective \
  -out /tmp/strap-schema-scripted
```

Add `-mode live -profile PROFILE -repeat 5` and choose a new output directory
to run the same family against the configured live server.

Trials run sequentially. Defaults are one repetition, eight model calls, 24 tool
calls, and three minutes per trial. Adjust them with `-repeat`, `-max-calls`,
`-max-tool-calls`, and `-timeout`. Live runs accept the ladder's catalog and model
flags, with `-model-timeout` controlling each model request because `-timeout`
bounds the whole trial. Scripted runs do not load the catalog.

The default destination is
`eval/results/interaction_<commit>_<profile>_<timestamp>`. An existing nonempty
destination is rejected: the pilot does not resume or mix experiments. Each run
writes saved trial results, traces, fixture manifests, and Markdown/JSON reports.
`interaction report` regenerates the reports from saved trial results. Run and
report return a nonzero exit status if any trial's outcome is not `passed`.
An empty report is treated as an incomplete run and also returns a nonzero status.
The run records its planned trial count; a partial run cannot be reported as a
complete success merely because its finished trials passed.

## Read the scores

- **Harness:** explicit assertions about the exercised transitions, rejection
  guards, state invariants, and trace reconstruction. Live trials establish only
  the guards they actually exercise.
- **Script/model behavior:** correct outcome, clean success, bounded recovery,
  rejected calls, and unknown tool errors. Expected negative scripts can pass
  conformance while recording `clean=false`; they are not model observations.
- **Schema behavior:** first operation's tool choice, argument validity against
  the selected tool's advertised schema, and invalid contract calls across all
  attempted operations. Read-only discovery does not consume the first-operation
  score. Recovery preserves the original first-operation scores. Reports show
  unattempted and unscorable trials explicitly; first-operation denominators
  include only scorable schema trials with an attempted operation.
- **Trial validity and efficiency:** stop reason, error classification, calls,
  duration, and available token usage. An unavailable usage value remains absent.

For the race case, the forced rejection is the experimental challenge, not an
avoidable mistake by the model. A passing result is reported as recovered rather
than clean. Compare recovery success and additional rejected calls for this case;
do not compare its clean score directly with cases that can avoid rejection.

Setup events are outside the actor's grading interval. The runner observes the
complete tool batch that completes the target operation, then gates further
model execution.
An earlier reply, yield, agent exit, budget limit, or timeout also ends the trial.
It pins the trace prefix before cleanup, so neither fixture setup nor cleanup
can satisfy or invalidate the actor's assertions. A successful tool call alone
is not enough to pass; later effects inside that batch count.

Failures preserve assertion IDs, expected and actual values, and trace cursors
for inspection. The fixture, effective configuration, and executable, prompt,
and tool-schema hashes identify the experiment. Repeat live trials to compare
behavior; this pilot does not claim statistical reliability from a single pass.

The [implementation plan](../../docs/evals/interaction-eval-plan.md) describes
the next stages: delegation and submission, audit judgment over controlled
artifacts, repair cycles, fault and race recovery, generated action sequences,
and short multi-agent workflows followed by the practical ladder.

## What each check establishes

Independent schema conformance tests live in the `tool` package. They validate
exported JSON Schema with a separate validator and compare its structural
acceptance with the actual decoder. These tests cover operation fields, required
values, unknown fields, nulls, nested shapes, and exact numeric bounds without
calling a model or relying on domain state.

This interaction suite runs the actual session, tools, domain guards, and recorded
state transitions. Audit-assignment state checks decode arguments through the
production contract to identify the intended command. Schema regression cases
also use an independent JSON Schema validator on the actor's actual advertised
catalog; they compare that contract with dispatcher rejection and state effects.
The package-level conformance tests cover a broader structural matrix; the evals
add role selection, recovery, and real workflow state. Neither suite proves
universal schema correctness or model reliability.
Current HTTP assignment routes use the same operation contracts. Other HTTP
commands, including audit-verdict decoding, need their own parity checks.

Scripted providers directly return tool calls, so they bypass the live server and
its wire parser. The adversarial malformed-output test injects a provider error;
it establishes recovery classification, not the behavior of a server emitting
truncated JSON. Live mode uses the configured provider and real model server, but
controlled collaborators make no model requests. Most negative guard assertions
require rejection and no state change; the revision-race test additionally checks
the actual conflict response and recovery. Error classification beyond those
checks remains a separate extension.
