# Model tool schema review

Status: clean architectural replacement, September 16, 2026. The assignment
interface now uses four operation-specific tools and matching HTTP routes. The
old tool and route are removed; no compatibility catalog or tagged JSON form is
maintained. Historical observations below retain their original tool names.

## Finding

The previous model-facing adapters for `assign_work` and `submit_audit` had a
concrete contract mismatch: their published schemas allowed structural shapes
that their argument decoders rejected. The workflow and ledger protected
state, but the interface made the model infer conditional input rules from prose
or learn them through rejected calls.

The defect is narrower than all schema-related failures. This change does not fix
truncated output, insufficient output budgets, invalid IDs, stale revisions, nested
progress-report mistakes, or unbounded retry behavior. It has not yet been shown
to improve model reliability in a controlled comparison.

## Evidence and cause

Before this fix, `tool.ComposeBy` took strict branch contracts and published a
flat union of their fields. It required the discriminator and only fields required
by every branch. Unlike ordinary `Compose`, it discarded the authoritative `oneOf`.
`compositionHints` deliberately does not merge object required fields or
`additionalProperties`; differing array alternatives can lose cardinality rules.
This produced an approximation instead of a complete structural contract.

The pre-fix exported `assign_work` schema had only `properties`, `required`, and
`type` at its root. Its required list was `kind, assignee`. The `submit_audit`
schema had the same root keys and required `verdict, expected_revision,
submission_id, summary, work_id`. Neither forbade unknown fields. The emitted
`findings` schema did not express the pass/fail cardinality constraints.

A temporary diagnostic invoked the actual production argument decoders with five
counterexamples and two positive controls. No domain handler ran for any rejected
case; both positive controls reached their handlers.

| Request shape | Published structural schema | Actual argument decoder |
| --- | --- | --- |
| Audit with only kind and assignee | Allows | Rejects missing work/revision/submission |
| Audit with valid submission plus audit_id | Allows | Rejects repair-only field |
| Audit with an invented extra field | Allows | Rejects unknown field |
| Fail verdict without findings | Allows | Rejects missing findings |
| Pass verdict with nonempty findings | Allows | Rejects more than zero findings |

The published-schema column follows inspection of the exported schemas and JSON
Schema semantics; this diagnostic did not use an independent JSON Schema engine.
The decoder column comes from execution, without a model or domain-state fixture.
[Probe source](../../eval/results/schema-contract-audit-20260916/probe.go) and
[full schemas, inputs, and results](../../eval/results/schema-contract-audit-20260916/contract-probe.json)
are saved as ignored local artifacts.

The standard treats properties as optional unless required and permits additional
properties unless restricted. Descriptions do not supply these validation rules.
See [JSON Schema object rules](https://json-schema.org/understanding-json-schema/reference/object).

There is direct live evidence of the matching failure: in the earlier Qwen pilot,
the model named the correct current submission in prose, then called
`assign_work(kind=audit)` using `audit_id`. The decoder rejected the field and the
model corrected it to `submission_id`. See the
[recorded response](../../eval/results/interaction-pilot-qwen38-20260917T014654833Z/audit-stale-revision/001/trace.jsonl)
at sequences 79, 83, and 88. The subsequent 20 guided trials did not reproduce it;
the trace establishes that the failure occurs, not its general frequency.

## Why the previous design existed

Commit `a33bdb4` flattened the discriminator-based compositions after a model
omitted `kind` from an implementation assignment and received contradictory
errors from multiple branches. Requiring a visible discriminator and reporting
only its branch's error addressed a real issue. Dropping structural constraints
was a consequential tradeoff, not an absence of validation code.

Commit `ce24316` previously split the composed plan-editing interface into
`create_plan`, `add_step`, `edit_step`, `cancel_steps`, `reorder_steps`, and
`rename_plan`. Those tools still share the same domain update operation. Its
recorded motivation was widespread form mixing and copying read snapshots into
write commands. This is an existing architectural precedent, not a controlled
measurement of the new assignment interface.

The original explicit-agent design intended schema generation and JSON decoding
to share one contract. They shared source definitions, but the lossy schema
transformation broke agreement in the artifact supplied to the model.

## Implemented boundary

Expose one command per assignment operation:

| Model tool and HTTP action | Parameters |
| --- | --- |
| `assign_implementation` | assignee, task; optional context, expected_output, scope |
| `assign_research` | assignee, task; optional context, expected_output |
| `assign_audit` | assignee, original work_id, expected_revision, submission_id |
| `assign_repair` | assignee, original work_id, expected_revision, failed audit_id |

The tool name selects the operation. Payloads contain no `kind`, and each
schema states its required fields and forbids extra fields directly. Each operation
owns one typed `Parameters` contract. Both its model tool and
`DecodeAssignment(name, raw)` use that contract, then normalize to the shared
`work.AssignmentRequest`. The adapter supplies the internal kind. The existing
Go workflow operation remains useful; it is not a second JSON input contract.

The old `assign_work` tool, `AssignWorkArgs` alias, and `/work/assign` route are
removed. Assignment HTTP callers select the corresponding `/work/assign_*`
action and send the same payload as the tool. Current prompts, callers, and eval
fixtures migrate together. There is no runtime decoder for historical assignment
calls. Saved historical reports and traces remain evidence of their original runs.

`ComposeBy` now retains the complete `oneOf` branches as well as discriminator
hints. `submit_audit` continues to use a verdict discriminator: pass permits only
omitted or empty findings, while fail requires findings. Numeric schema edits
preserve raw JSON values so uint64 revision bounds are not rounded through float64.
Splitting verdict tools is outside this change.

`Parameters.Decode` no longer removes null-valued fields or array elements before
validation. Optional means omitted, not null. The progress adapter no longer moves
a top-level `objective` into `position` or round-trips numbers through float64.
A malformed progress request receives the declared contract's validation error.

The workflow and ledger retain authority checks, role eligibility, audit
independence, optimistic concurrency, immutable submissions, and all domain state
transitions. These are separate from static JSON structure. The HTTP assignment
routes share the exact assignment contracts; this does not establish global HTTP
schema parity. In particular, the existing HTTP audit-verdict decoding path is
not replaced by this change.

## Architectural rules

1. **Preserve structural constraints when exporting a contract.** A schema
   compiler must not silently weaken required fields, allowed fields, enum values,
   bounds, or nested shapes. Composition must preserve its complete branches.
2. **Separate shape from state.** A structurally valid call can still fail because
   its actor lacks authority, its submission is stale, or its revision conflicts.
   Those remain expected domain validations and need distinct error categories.
3. **Reject unadvertised shapes.** Nulls and misplaced progress fields are not
   repaired into a different request. Integer-valued JSON numbers are interpreted
   exactly; malformed JSON, duplicate keys, and trailing values remain decoder
   errors before schema validation.
4. **Preserve intent commitments.** Keep submission IDs and expected revisions.
   Automatically replacing them with whatever is current at execution time can
   mutate a different subject from the one the model actually inspected. Derived
   task/scope and caller identity remain server-owned.
5. **Distinguish validation failures in evidence.** Missing field, forbidden field,
   wrong type, wrong operation, unknown tool, stale binding, and transport truncation
   should not all become an undifferentiated tool error.

## Alternatives and scope

An exact discriminated union is semantically valid and could retain the single
tool name. It could be studied in a separately versioned experiment, provided the
actual serving stack preserves and enforces it; it is not retained as a production
API. The earlier model difficulty is evidence against blindly reverting to the
old presentation, not proof that all union schemas fail.

Stronger prose on the previous flat schema would leave the contract mismatch.
Grammar-constrained output can enforce the supplied schema; it cannot recover
branch rules omitted from that schema. SGLang documents constraints against a
supplied JSON schema, with backend-dependent machinery; that does not establish
the capabilities or configuration of our particular running server.
[SGLang structured output documentation](https://docs.sglang.io/docs/advanced_features/structured_outputs)

The other composed production readers (`list_work`, `get_work_progress`, and
`get_research_brief`) use ordinary `Compose` and retain `oneOf`. Audit their model
usability and provider compatibility separately rather than splitting every tool
preemptively. Rich nested reports also need their own input-ergonomics tests.

## Verification and measurement

The [schema conformance tests](../../tool/schema_conformance_test.go) use an
independent JSON Schema validator to compare exported schemas with the actual
argument decoder. It checks canonical
positive examples and one-fault mutations: omissions, extra fields, wrong types,
operation mixing, enum values, nulls, nested required fields, cardinalities, and
numeric bounds. Exact integer tests cover values beyond float64 precision. The
validator must inspect the published schema, not reconstruct constraints from the
same decoder; otherwise the two sides could repeat the same defect.

Structural tests use valid placeholder identifiers and handlers without state
requirements. They establish the schema/decoder boundary. Workflow and HTTP tests
separately establish routing, normalized command effects, authority, current
revision, submission/audit binding, and rejection without mutation. A valid schema
never promises a successful mutation in arbitrary state. Semantic restrictions
such as whitespace-only domain values are also separate from JSON shape.

The interaction pilot uses the new `assign_audit` payload throughout its scripted
providers, boundary, grader, and race trigger. All five scenarios are version 2;
their semantic state assertions are unchanged. Revision bookkeeping remains part
of repeated-call detection. Version 1 traces and analysis documents retain the
old names and original evidence and are not regraded as version 2 trials.

Migration validation passed the tool, workflow, harness, HTTP, eval, CLI, and example
tests, plus `go vet` for those packages. Race checks passed for tool contracts
(excluding the unrelated shell process cleanup test), workflow, harness, HTTP,
interaction evals, and CLI. The ladder runner's idle-session test also passed three
consecutive trials after its scripted caller migrated to `assign_implementation`.
The full `go test ./...` run has one unrelated failure: the existing bundled-profile
test expects 131072 output tokens while the pre-existing local Qwen profile sets
32000. That profile was not changed by this implementation.

The new catalog also passed a five-scenario live smoke run against Qwen3.8-27b at
the configured server on port 8360. All five trials passed: the first four made no
rejected calls, and the revision-race trial recovered after one expected conflict.
The live run exercised 40/40 harness assertions. The corresponding scripted run
passed 5/5 trials and 51/51 assertions, including deliberate negative probes.
See the [live report](../../eval/results/schema-clean-cutover-live-server-20260916/report.md)
and [scripted report](../../eval/results/schema-clean-cutover-scripted-20260916/report.md).
An initial sandboxed live attempt could not connect to the server and produced
unscorable provider errors; it is kept separately from the completed live run.

These are smoke results, not a controlled reliability comparison. The runs record
executable, prompt, schema, and scenario identities. Future comparisons should
measure first operation choice, initial structural validity, semantic binding,
recovery attempts, unintended mutations, token volume, and latency. The new tools
add three root choices; test whether improved field selection introduces operation
selection errors. Stimuli should request the action without naming its tool.

The pilot covers audit assignment and revision recovery, not every operation.
Broader implementation/research/repair selection and nested progress reporting
need separate scenarios. The adversarial malformed-output regression injects a
provider error and does not validate actual server serialization or wire parsing.
Short live workflows and the practical ladder remain the composition checks.

## Code entry points

- [Exact discriminator schema export](../../tool/compose.go)
- [Single-source operation contracts and strict decoding](../../tool/work_contract.go)
- [Plan-tool precedent and audit/assignment tools](../../tool/work.go)
- [Typed schema generation and strict argument validation](../../tool/parameters.go)
- [Existing architectural contract](EXPLICIT_AGENT_WORK_DESIGN.md)
- [Interaction evaluation plan](../evals/interaction-eval-plan.md)
