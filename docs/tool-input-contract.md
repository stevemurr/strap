# Tool inputs: object-input-v1

All tools use `NewParameters`, including forms combined by `Compose` or
`ComposeBy`. The contract owns both the advertised schema and strict decoding.
Every tool takes exactly one closed `input` object, including empty tools
(`{"input":{}}`). `NewParameters[A]` adds this envelope once; `Func[A]` handlers
receive A. Go callers use `tool.MarshalInput(value)`. Every declared field is required, recursively. Every object is closed. Optional
values use explicit null; omission is an error. IDs remain explicit and non-null.

```go
type ReadArgs struct {
    Path  string `json:"path"`
    Limit *int   `json:"limit"`
}
parameters, err := tool.NewParameters[ReadArgs](
    tool.MinLength("path", 1),
    tool.Nullable("limit", "use the default page size"),
    tool.Minimum("limit", 1),
    tool.Maximum("limit", 100),
)
```

`{"input":{"path":"notes.txt","limit":null}}` requests the default.
`{"input":{"path":"notes.txt"}}` is invalid. Declaring a Go pointer does not make a value
nullable. `Nullable` requires a pointer or slice that can preserve null separately
from a concrete value, plus a description of what null means. JSON tag options
including `omitempty` are rejected on input types, including nested types.

Use slices for nullable arrays: nil means null and an allocated empty slice means
an empty array. Array element nullability is independent; existing elements are
non-null. Null, the string `"null"`, zero, false and empty collections are distinct.
Do not prefill missing fields or coerce values before validation.

Tool inputs are separate from persisted domain records. Explicit conversion at
the handler boundary applies the operation's documented semantics:

- Read limits/timeouts/cursors: null selects the documented default or first page.
- Replacement position fields: null clears the corresponding value. A null whole
  position preserves the prior position; a non-null position replaces it.
- Progress findings or steps: null adds no findings or step updates. At least one
  of position/findings/steps must be non-null; domain checks reject reports that contain no actual payload.
- Partial plan edits: null leaves that field unchanged. Creation/submission fields
  use null for absent optional information.

`report_work_progress` always carries work_id, position, findings and steps.
Nested position fields all appear, including a non-null objective. Evidence items
always carry uri and nullable revision/locator/detail. Existing domain checks for
ownership, scope, evidence, revision and legal transitions remain authoritative.
The Go domain API and stored records retain their existing serialization.
Model-command HTTP adapters accept `{"actor":"agent-id","request":{"input":{...}}}`
and reuse the same typed definitions and DTO-to-domain conversions as tools.
This covers agent creation, assignment, reassign, cancel, submit, research, audit,
and report-progress. The host-only `/work/plan` route retains atomic `PlanUpdate`
under `request`; messages, controls, inspections and results retain their formats.
The old `/work/progress` mutation and `ProgressUpdate` API are removed.

For distinct argument forms, compose complete typed tools. Each alternative owns
its entire closed object inside `input`; do not repeat properties alongside the union or add
fields from other alternatives as parser hints. `ComposeBy` requires a unique
non-null constant string discriminator. `AtLeastOneNonNull` uses complete `anyOf`
branches while permitting combinations of its members. Expansion is bounded.

Custom `Tool` implementations must expose `InputContract() tool.Contract`, built
with `NewParameters[A]().Contract()`, and advertise `Contract.Schema()` unchanged.
Agent registration rejects raw-schema tools, uninitialized contracts and schema
mismatches. Dispatch validates arguments before calling custom handlers. Prefer
`Func[A]`, which also decodes to A and validates direct calls. Custom tools called
directly outside the agent must validate their inputs themselves. Definitions and
contracts must remain stable after registration.

Both chat providers send the same schema and `function.strict:true` for every
tool. The old strict-tools CLI option and strict_tools/strict_all_tools config
fields are removed. There is no selective strictness or omission-compatible mode.
Session configuration records tool_contract_version and each role's schema_hash.

This is a breaking protocol change with no legacy-call or session conversion.
Start new sessions with the matched schema and backend release. Historical
transcripts remain readable; completed side effects must not be replayed.

Deployment requires qualification of the matched Strap and backend builds with
MTP enabled, `tool_choice: auto`, and all tools strict. Verify template, grammar,
parser, streaming, actual generation and application outcomes. Model copying
fidelity is excluded from schema acceptance; deterministic parsing of supplied
serialized values must still preserve them. Phase 4 migrates the application;
Phase 5 qualifies the final system before rollout.
