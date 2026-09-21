package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stevemurr/strap/work"
)

func compileExportedSchema(t *testing.T, raw json.RawMessage) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const location = "https://strap.test/tool.schema.json"
	if err := compiler.AddResource(location, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateExportedSchema(t *testing.T, schema *jsonschema.Schema, raw json.RawMessage) error {
	t.Helper()
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return schema.Validate(value)
}

// The test cases describe structural validity, not authority or ledger state.
// Assignment normalization also rejects whitespace-only semantic identifiers;
// that separate behavior is exercised by the wire/tool decoder tests.
func TestAssignmentSchemasAndTypedDecodersConform(t *testing.T) {
	calls := 0
	handle := func(context.Context, Call, work.AssignmentRequest) (Result, error) {
		calls++
		return Text("accepted"), nil
	}
	operations := map[string]Tool{}
	for _, op := range AssignmentTools(handle) {
		operations[op.Definition().Name] = op
	}
	for _, tc := range []struct {
		name     string
		seed     string
		required []string
		invalid  []schemaMutation
	}{
		{
			"assign_implementation",
			`{"input":{"assignee":"worker","task":"implement","context":"background","expected_output":"patch","scope":{"plan_id":"p","step_ids":["s"]}}}`,
			[]string{"assignee", "task"},
			[]schemaMutation{
				{"mixed submission", "submission_id", `"s"`},
				{"mixed revision", "expected_revision", `1`},
				{"legacy discriminator", "kind", `"implementation"`},
				{"empty task", "task", `""`},
				{"wrong context", "context", `42`},
				{"wrong scope", "scope", `42`},
				{"missing plan", "scope.plan_id", ""},
				{"nested extra", "scope.owner", `"forged"`},
				{"no steps", "scope.step_ids", `[]`},
				{"empty step", "scope.step_ids", `[""]`},
				{"null step", "scope.step_ids", `["s",null]`},
			},
		},
		{
			"assign_audit",
			`{"input":{"assignee":"auditor","work_id":"w","expected_revision":2,"submission_id":"s"}}`,
			[]string{"assignee", "work_id", "expected_revision", "submission_id"},
			[]schemaMutation{
				{"mixed audit id", "audit_id", `"a"`},
				{"mixed task", "task", `"audit this"`},
				{"legacy discriminator", "kind", `"audit"`},
				{"zero revision", "expected_revision", `0`},
				{"fractional revision", "expected_revision", `1.5`},
				{"string revision", "expected_revision", `"2"`},
				{"overflow revision", "expected_revision", `18446744073709551616`},
			},
		},
		{
			"assign_repair",
			`{"input":{"assignee":"worker","work_id":"w","expected_revision":2,"audit_id":"a"}}`,
			[]string{"assignee", "work_id", "expected_revision", "audit_id"},
			[]schemaMutation{
				{"mixed submission", "submission_id", `"s"`},
				{"mixed scope", "scope", `{"plan_id":"p","step_ids":["s"]}`},
				{"empty audit", "audit_id", `""`},
				{"negative revision", "expected_revision", `-1`},
			},
		},
		{
			"assign_research",
			`{"input":{"assignee":"researcher","task":"investigate","context":"background","expected_output":"findings"}}`,
			[]string{"assignee", "task"},
			[]schemaMutation{
				{"mixed scope", "scope", `{"plan_id":"p","step_ids":["s"]}`},
				{"mixed work", "work_id", `"w"`},
				{"wrong optional", "expected_output", `42`},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := operations[tc.name]
			if op == nil {
				t.Fatalf("missing %s", tc.name)
			}
			var decode func(json.RawMessage) error
			switch tc.name {
			case "assign_implementation":
				decode = exportedParameterDecoder[AssignImplementationArgs](t, op)
			case "assign_audit":
				decode = exportedParameterDecoder[AssignAuditArgs](t, op)
			case "assign_repair":
				decode = exportedParameterDecoder[AssignRepairArgs](t, op)
			case "assign_research":
				decode = exportedParameterDecoder[AssignResearchArgs](t, op)
			}
			schema := compileExportedSchema(t, op.Definition().Parameters)
			check := func(t *testing.T, raw json.RawMessage, want bool) {
				t.Helper()
				schemaErr, decodeErr := validateExportedSchema(t, schema, raw), decode(raw)
				before := calls
				_, callErr := op.Call(context.Background(), Call{Arguments: raw})
				_, wireErr := DecodeAssignment(tc.name, raw)
				if (schemaErr == nil) != want || (decodeErr == nil) != want || (callErr == nil) != want || (wireErr == nil) != want {
					t.Fatalf("want accepted=%v\ninput=%s\nschema=%v\nparameters=%v\ncall=%v\nwire=%v", want, raw, schemaErr, decodeErr, callErr, wireErr)
				}
				if got := calls - before; want && got != 1 || !want && got != 0 {
					t.Fatalf("handler called %d times for accepted=%v", got, want)
				}
			}
			check(t, json.RawMessage(tc.seed), true)
			mutations := append([]schemaMutation{}, tc.invalid...)
			mutations = append(mutations, schemaMutation{"unknown field", "owner", `"forged"`}, schemaMutation{"unknown null", "owner", `null`})
			for _, field := range tc.required {
				mutations = append(mutations, schemaMutation{"missing " + field, field, ""}, schemaMutation{"null " + field, field, `null`})
			}
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) { check(t, mutateSchemaSeed(t, tc.seed, mutation), false) })
			}
			if tc.name == "assign_audit" || tc.name == "assign_repair" {
				for _, revision := range []string{"1.0", "1e0", "9007199254740991", "90071992547409910e-1"} {
					t.Run("valid revision "+revision, func(t *testing.T) {
						check(t, mutateSchemaSeed(t, tc.seed, schemaMutation{path: "expected_revision", replacement: revision}), true)
					})
				}
			}
		})
	}
}

func exportedParameterDecoder[A any](t *testing.T, op Tool) func(json.RawMessage) error {
	t.Helper()
	f, ok := op.(Func[A])
	if !ok {
		t.Fatalf("%s does not expose its typed Parameters", op.Definition().Name)
	}
	return func(raw json.RawMessage) error { _, err := f.Spec.Parameters.Decode(raw); return err }
}

type schemaMutation struct{ name, path, replacement string }

// Mutate one property of a valid literal. RawMessage preserves integer tokens;
// these cases do not reuse the production validator to decide expected validity.
func mutateSchemaSeed(t *testing.T, seed string, mutation schemaMutation) json.RawMessage {
	t.Helper()
	var apply func(json.RawMessage, []string) json.RawMessage
	apply = func(raw json.RawMessage, path []string) json.RawMessage {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		if len(path) > 1 {
			object[path[0]] = apply(object[path[0]], path[1:])
		} else if mutation.replacement == "" {
			delete(object, path[0])
		} else {
			object[path[0]] = json.RawMessage(mutation.replacement)
		}
		out, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	return apply(json.RawMessage(seed), strings.Split("input."+mutation.path, "."))
}

func TestAuditSchemaAndDispatchConform(t *testing.T) {
	calls := 0
	op := SubmitAudit(func(context.Context, Call, work.AuditRequest) (Result, error) { calls++; return Text("ok"), nil })
	schema := compileExportedSchema(t, op.Definition().Parameters)
	pass := `{"input":{"work_id":"w","expected_revision":2,"submission_id":"s","verdict":"pass","summary":"checked","findings":null}}`
	fail := `{"input":{"work_id":"w","expected_revision":2,"submission_id":"s","verdict":"fail","summary":"broken","findings":[{"description":"missing check","required_change":"add check","verification":"run test","step_ids":null}]}}`
	cases := []struct {
		name, raw string
		valid     bool
	}{{"pass", pass, true}, {"fail", fail, true}}
	for _, mutation := range []schemaMutation{
		{"missing verdict", "verdict", ""},
		{"unknown verdict", "verdict", `"maybe"`},
		{"null verdict", "verdict", `null`},
		{"authority", "assignee", `"forged"`},
		{"overflow", "expected_revision", `18446744073709551616`},
		{"empty work", "work_id", `""`},
		{"wrong findings", "findings", `42`},
		{"nonempty pass findings", "findings", `[{"description":"d","required_change":"c","verification":"v"}]`},
	} {
		cases = append(cases, struct {
			name, raw string
			valid     bool
		}{mutation.name, string(mutateSchemaSeed(t, pass, mutation)), false})
	}
	for _, mutation := range []schemaMutation{
		{"missing failed findings", "findings", ""},
		{"empty failed findings", "findings", `[]`},
		{"missing nested verification", "findings", `[{"description":"d","required_change":"c"}]`},
		{"empty nested change", "findings", `[{"description":"d","required_change":"","verification":"v"}]`},
		{"extra nested field", "findings", `[{"description":"d","required_change":"c","verification":"v","owner":"forged"}]`},
		{"null nested entry", "findings", `[null]`},
	} {
		cases = append(cases, struct {
			name, raw string
			valid     bool
		}{mutation.name, string(mutateSchemaSeed(t, fail, mutation)), false})
	}
	for _, revision := range []string{"1.0", "9007199254740991", "90071992547409910e-1"} {
		cases = append(cases, struct {
			name, raw string
			valid     bool
		}{"exact revision " + revision, string(mutateSchemaSeed(t, pass, schemaMutation{path: "expected_revision", replacement: revision})), true})
	}
	cases = append(cases, struct {
		name, raw string
		valid     bool
	}{"explicit empty pass findings", string(mutateSchemaSeed(t, pass, schemaMutation{path: "findings", replacement: `[]`})), true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := calls
			schemaErr := validateExportedSchema(t, schema, json.RawMessage(tc.raw))
			_, callErr := op.Call(context.Background(), Call{Arguments: []byte(tc.raw)})
			if (schemaErr == nil) != tc.valid || (callErr == nil) != tc.valid {
				t.Fatalf("want accepted=%v, schema=%v call=%v input=%s", tc.valid, schemaErr, callErr, tc.raw)
			}
			if got := calls - before; tc.valid && got != 1 || !tc.valid && got != 0 {
				t.Fatalf("handler called %d times for valid=%v", got, tc.valid)
			}
		})
	}
}
