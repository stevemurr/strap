# Schema regression evals

Date: September 16, 2026, America/Los_Angeles. Artifact timestamps use UTC.

Eight new interaction scenarios exercise the historical schema failures through
the production role catalogs, tool dispatcher, and work state transitions. They
extend the five existing assignment scenarios and run in the normal Go test suite.

## Results

| Run | Outcome | Harness assertions | First tool / first arguments |
| --- | --- | --- | --- |
| Scripted, all 13 scenarios | 13/13 passed | 131/131 | New schema cases deliberately start invalid: 7/8 correct tools, 0/8 valid arguments. |
| Live, eight schema scenarios | 8/8 clean successes | 40/40 | 8/8 correct tools; 8/8 valid arguments. |

The scripted schema cases each sent one invalid call and then a valid correction.
All eight invalid calls were rejected without domain changes, and all eight
corrections produced the required state. The removed tool produced an unknown-tool
error; the other seven produced argument-validation errors. The 131 assertion
instances include the existing scenarios, and are not 131 distinct contracts.

The live run used the configured Qwen3.8-27B server at
`http://model.internal:8360`, profile `qwen3.8-27b`, with thinking enabled and
reasoning effort `xhigh`. It made 15 model requests and 18 tool calls, including
read-only discovery. All eight first operations used the correct tool and valid
arguments. No rejection or recovery was needed. These are one trial per case,
so they establish a smoke-test result rather than a reliable failure rate.

## Seams covered

| Scenario | Historical failure | Required result |
| --- | --- | --- |
| `schema-audit-repair-field` | `audit_id` used instead of `submission_id` | Reject, then bind an audit to the latest submission. |
| `schema-audit-kind` | Removed `kind` discriminator retained | Reject, then accept the operation-specific request. |
| `schema-audit-required` | Required work/revision/submission fields omitted | Reject, then accept complete bindings. |
| `schema-audit-null-extra` | Unknown field hidden by a null value | Reject without stripping the field. |
| `schema-removed-assignment-tool` | Obsolete `assign_work` selected | Reject unknown tool, then use `assign_audit`. |
| `schema-audit-fail-findings` | Failed verdict lacks findings | Reject, then record a failed audit with actionable findings and request changes. |
| `schema-audit-pass-findings` | Passed verdict contains failure findings | Reject, then record a passed audit and complete the scoped plan step. |
| `schema-progress-objective` | Top-level objective silently relocated | Reject, then record the requested position with exact work/revision bindings. |

## Methodology

Fixtures are created through public session operations. The root, auditor, or
implementor receives its actual production prompt and tools; controlled
collaborators do not make model requests. Auditor cases start after a real purpose
and verification report, with simple arithmetic evidence. They target encoding
and recording the final verdict, not general review quality.

An independent JSON Schema validator checks the actual advertised catalog. The
oracle then checks the real dispatcher response, every domain change and ledger
record, the success receipt, and replay of the saved trace. Rejected calls must
produce no changes, including orphan records. Successful calls must produce the
specific transition without collateral effects. The grading interval includes
the entire final tool batch and excludes setup and cleanup.

First-tool selection and first-argument validity are separate from eventual
success. Reads do not consume the first-operation score, and a later correction
does not erase an initial error. Setup/provider failures remain visible as
excluded trials. A mixed control-tool batch is checked as a batch rejection;
it does not count as exercising the argument decoder.

Adversarial grader tests ensure that missing probes, wrong errors, incorrect
bindings or receipts, orphan records, broadened schemas, replay mismatches,
and later side effects cannot yield a false pass. They also cover compound
malformed calls, equivalent integer notation, and incomplete progress content.
All eval and CLI tests, race checks, and vet checks passed.

Scripted trials bypass the live provider and wire parser. The live run does use
the real server, but avoided every malformed call, so rejection guarantees come
from the scripted tests. HTTP decoding, malformed server output, broad schema
fuzzing, and general audit judgment remain outside these eight scenarios. The
existing `tool` conformance tests cover a broader structural input matrix.

## Evidence and reproduction

- [Final scripted report](../../eval/results/schema-regressions-scripted-20260916-final/report.md)
- [Live report](../../eval/results/schema-regressions-live-20260916/report.md)
- [Usage and scoring guide](../../eval/interaction/README.md#schema-regressions)
- [Scenario fixtures](../../eval/interaction/schema_fixture.go)
- [Independent state oracle](../../eval/interaction/schema_oracle.go)

The local result directories are ignored artifacts. Each contains manifests,
traces, reports, and executable/prompt/schema fingerprints. Preserve them when
sharing this analysis. The live run predates final grader hardening; its saved
report has not been rewritten. Its progress call was inspected and contains
exactly the requested objective, note, and next step, satisfying the tightened
progress check. The other new grader edge cases did not occur in the live traces.

Run the complete deterministic suite with:

```sh
go test ./eval/... ./cmd/strap-eval
go run ./cmd/strap-eval interaction run -out /tmp/strap-schema-scripted
```

The usage guide lists the eight schema IDs for a focused live run. The recorded
live run used `-mode live -profile qwen3.8-27b -repeat 1 -timeout 90s`; use a new
output directory for each experiment.
