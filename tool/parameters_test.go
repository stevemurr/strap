package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stevemurr/strap/provider"
)

type contractStep struct {
	ID     string  `json:"step_id"`
	Status *string `json:"status"`
}
type contractArgs struct {
	Title string         `json:"title"`
	Count *int8          `json:"count"`
	Steps []contractStep `json:"steps"`
	Pages []int          `json:"pages"`
}

// contractConstraints prefixes the nullable declarations every contractArgs
// contract needs with any extra constraints under test.
func contractConstraints(extra ...Constraint) []Constraint {
	return append([]Constraint{Nullable("count", "default"), Nullable("steps", "no steps"), Nullable("pages", "default pages"), Nullable("steps[].status", "unchanged")}, extra...)
}

func TestParametersValidateAndDecodeOneContract(t *testing.T) {
	choices := []string{"pending", "ready"}
	params, err := NewParameters[contractArgs](contractConstraints(Minimum("count", 1), Maximum("count", 10), MinItems("steps", 1), MinLength("steps[].step_id", 1), Enum("steps[].status", choices...), MinItems("pages", 1), MaxItems("pages", 2), Minimum("pages[]", 1))...)
	if err != nil {
		t.Fatal(err)
	}
	choices[0] = "mutated"
	for _, raw := range []string{`{"input":{"title":"p","pages":[1,1.0],"count":null,"steps":null}}`, `{"input":{"title":"","count":null,"steps":null,"pages":null}}`, `{"input":{"title":"plan","count":1.0,"steps":null,"pages":null}}`, `{"input":{"title":"plan","count":1e1,"steps":[{"step_id":"s","status":"pending"}],"pages":[2,1]}}`} {
		got, err := params.Decode(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got.Title == "" && got.Count != nil {
			t.Fatal("omission changed")
		}
	}
	for _, raw := range []string{`{"input":{}}`, `{"input":{"title":null,"count":null,"steps":null,"pages":null}}`, `{"input":{"title":"p","Title":"bypass","count":null,"steps":null,"pages":null}}`, `{"input":{"title":"p","title":"duplicate","count":null,"steps":null,"pages":null}}`, `{"input":{"title":"p","count":0,"steps":null,"pages":null}}`, `{"input":{"title":"p","count":11,"steps":null,"pages":null}}`, `{"input":{"title":"p","count":128,"steps":null,"pages":null}}`, `{"input":{"title":"p","count":1.5,"steps":null,"pages":null}}`, `{"input":{"title":"p","steps":"[]","count":null,"pages":null}}`, `{"input":{"title":"p","steps":[],"count":null,"pages":null}}`, `{"input":{"title":"p","steps":[{"status":null}],"count":null,"pages":null}}`, `{"input":{"title":"p","steps":[{"step_id":"s","note":"escape","status":null}],"count":null,"pages":null}}`, `{"input":{"title":"p","steps":[{"step_id":"s","status":"completed"}],"count":null,"pages":null}}`, `{"input":{"title":"p","steps":[{"step_id":"s","status":"done"}],"count":null,"pages":null}}`, `{"input":{"title":"p","pages":[1,2,3],"count":null,"steps":null}}`, `{"input":{"title":"p","pages":[],"count":null,"steps":null}}`, `{"input":{"title":"p"} {}}`} {
		if _, err := params.Decode(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(inputSchema(t, params.Schema()), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	count := props["count"].(map[string]any)
	if count["minimum"] != float64(1) || count["maximum"] != float64(10) {
		t.Fatal(count)
	}
	if got := schema["required"].([]any); len(got) != 4 {
		t.Fatal(got)
	}
	original := string(params.Schema())
	raw := params.Schema()
	raw[0] = 'x'
	if string(params.Schema()) != original {
		t.Fatal("schema storage leaked")
	}
	if _, err := params.Decode([]byte(`{"input":{"title":"p","count":11,"steps":null,"pages":null}}`)); err == nil {
		t.Fatal("export changed validation")
	}
}

type recursiveArgs struct {
	Next *recursiveArgs `json:"next"`
}
type customInput string

func (*customInput) UnmarshalJSON([]byte) error { return nil }
func TestParametersRejectUnsupportedDefinitions(t *testing.T) {
	checks := []func() error{
		func() error { _, err := NewParameters[map[string]string](); return err },
		func() error { _, err := NewParameters[recursiveArgs](); return err },
		func() error { _, err := NewParameters[struct{ Value any }](); return err },
		func() error { _, err := NewParameters[struct{ Value float64 }](); return err },
		func() error { _, err := NewParameters[struct{ Value []byte }](); return err },
		func() error { _, err := NewParameters[struct{ Value customInput }](); return err },
		func() error {
			// Construct the deliberately invalid type dynamically so go vet can
			// still check the actual source declarations for accidental tag drift.
			duplicate := reflect.StructOf([]reflect.StructField{
				{Name: "A", Type: reflect.TypeFor[string](), Tag: `json:"x"`},
				{Name: "B", Type: reflect.TypeFor[string](), Tag: `json:"x"`},
			})
			_, err := compileParameter(duplicate, map[reflect.Type]bool{})
			return err
		},
		func() error {
			_, err := NewParameters[struct {
				Value int `json:"value,string"`
			}]()
			return err
		},
		func() error {
			_, err := NewParameters[contractArgs](contractConstraints(Minimum("missing", 1))...)
			return err
		},
		func() error {
			_, err := NewParameters[contractArgs](contractConstraints(Minimum("title", 1))...)
			return err
		},
		func() error {
			_, err := NewParameters[contractArgs](contractConstraints(Maximum("count", 128))...)
			return err
		},
		func() error {
			_, err := NewParameters[contractArgs](contractConstraints(Minimum("count", 10), Maximum("count", 1))...)
			return err
		},
		func() error {
			_, err := NewParameters[contractArgs](contractConstraints(MinItems("steps", 3), MaxItems("steps", 1))...)
			return err
		},
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("accepted invalid definition %d", i)
		}
	}
	var zero Parameters[contractArgs]
	if _, err := zero.Decode([]byte(`{"input":{"title":"p","count":null,"steps":null,"pages":null}}`)); err == nil {
		t.Fatal("accepted uninitialized contract")
	}
}

// Wider Go types advertise only the range a JSON number carries exactly, and
// the decoder holds the same line. Values just past that bound are rejected
// rather than rounded down onto it, which is what a float64 decoder would do.
func TestIntegerContractPreservesExactValues(t *testing.T) {
	type args struct {
		Signed   int64  `json:"signed"`
		Unsigned uint64 `json:"unsigned"`
	}
	p := parameters[args]()
	for _, raw := range []string{
		`{"input":{"signed":-9007199254740991,"unsigned":9007199254740991}}`,
		`{"input":{"signed":-9007199254740991.0,"unsigned":90071992547409910e-1}}`,
		`{"input":{"signed":0e99999999999999999999,"unsigned":0e-99999999999999999999}}`,
	} {
		if _, err := p.Decode([]byte(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"input":{"signed":9007199254740992,"unsigned":0}}`, `{"input":{"signed":0,"unsigned":9007199254740992}}`, `{"input":{"signed":9223372036854775807,"unsigned":0}}`, `{"input":{"signed":0,"unsigned":18446744073709551615}}`, `{"input":{"signed":0,"unsigned":-1}}`, `{"input":{"signed":1e99999999999999,"unsigned":0}}`, `{"input":{"signed":1e-99999999999999,"unsigned":0}}`} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestFuncRejectsInvalidCallsBeforeHandler(t *testing.T) {
	calls := 0
	f := Func[contractArgs]{Spec: Definition[contractArgs]{Name: "example", Parameters: parameters[contractArgs](contractConstraints()...)}, Invoke: func(_ context.Context, c Call, a contractArgs) (Result, error) {
		calls++
		if c.Actor != "root" || a.Title != "ok" {
			t.Fatal(c, a)
		}
		return Text("done"), nil
	}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call(context.Background(), Call{Arguments: []byte(`{"input":{}}`)}); err == nil {
		t.Fatal("missing field accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Call(ctx, Call{Arguments: []byte(`{"input":{"title":"ok","count":null,"steps":null,"pages":null}}`)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("invalid call reached handler")
	}
	if _, err := f.Call(context.Background(), Call{Actor: "root", Arguments: []byte(`{"input":{"title":"ok","count":null,"steps":null,"pages":null}}`)}); err != nil {
		t.Fatal(err)
	}
	f.Invoke = nil
	if err := f.Validate(); err == nil {
		t.Fatal("nil handler")
	}
}
func TestComposePreservesBranchesAndRejectsAmbiguity(t *testing.T) {
	type createArgs struct {
		Title string `json:"title"`
	}
	type editArgs struct {
		ID string `json:"plan_id"`
	}
	calls := 0
	create := Func[createArgs]{Spec: Definition[createArgs]{Name: "create", Parameters: parameters[createArgs]()}, Invoke: func(context.Context, Call, createArgs) (Result, error) { calls++; return Text("create"), nil }}
	edit := Func[editArgs]{Spec: Definition[editArgs]{Name: "edit", Parameters: parameters[editArgs]()}, Invoke: func(context.Context, Call, editArgs) (Result, error) { calls++; return Text("edit"), nil }}
	composed, err := Compose(provider.ToolDefinition{Name: "update"}, &create, edit)
	if err != nil {
		t.Fatal(err)
	}
	// Composition snapshots the definition and handler, including pointer inputs.
	create.Invoke = func(context.Context, Call, createArgs) (Result, error) {
		t.Fatal("mutated handler used")
		return Result{}, nil
	}
	for raw, want := range map[string]string{`{"input":{"title":"p"}}`: "create", `{"input":{"plan_id":"p"}}`: "edit"} {
		got, err := composed.Call(context.Background(), Call{Arguments: []byte(raw)})
		if err != nil || got.Content.Text() != want {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{`{"input":{}}`, `{"input":{"title":"p","plan_id":"p"}}`, `{"input":{"title":1}}`} {
		if _, err := composed.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal(raw)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	other := edit
	other.Spec.Name = "another_edit"
	ambiguous, err := Compose(provider.ToolDefinition{Name: "ambiguous"}, edit, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ambiguous.Call(context.Background(), Call{Arguments: []byte(`{"input":{"plan_id":"p"}}`)}); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("ambiguous call executed")
	}
	var schema map[string]any
	if err := json.Unmarshal(inputSchema(t, composed.Definition().Parameters), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema["oneOf"].([]any)) != 2 {
		t.Fatal(schema)
	}
	raw := composed.Definition().Parameters
	raw[0] = 'x'
	if !json.Valid(composed.Definition().Parameters) {
		t.Fatal("composition schema leaked")
	}
}
func TestParametersSharedAcrossCalls(t *testing.T) {
	p := parameters[contractArgs](contractConstraints()...)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if _, err := p.Decode([]byte(`{"input":{"title":"p","count":null,"steps":null,"pages":null}}`)); err != nil {
					t.Error(err)
				}
				p.Schema()
			}
		}()
	}
	wg.Wait()
}

func TestTypedParametersCannotBeConvertedAcrossArgumentTypes(t *testing.T) {
	type a struct{ Title string }
	type b struct{ Count int }
	if reflect.TypeFor[Parameters[a]]().ConvertibleTo(reflect.TypeFor[Parameters[b]]()) {
		t.Fatal("argument type can be changed without rebuilding the contract")
	}
}
func TestCompositionPreservesIntegerBoundsWithoutHints(t *testing.T) {
	type first struct {
		Steps    []string `json:"steps"`
		Revision uint64   `json:"revision"`
	}
	type second struct {
		PlanID string `json:"plan_id"`
	}
	f := Func[first]{Spec: Definition[first]{Name: "first", Parameters: parameters[first]()}, Invoke: func(context.Context, Call, first) (Result, error) { return Result{}, nil }}
	g := Func[second]{Spec: Definition[second]{Name: "second", Parameters: parameters[second]()}, Invoke: func(context.Context, Call, second) (Result, error) { return Result{}, nil }}
	op, err := Compose(provider.ToolDefinition{Name: "combined"}, f, g)
	if err != nil {
		t.Fatal(err)
	}
	raw := op.Definition().Parameters
	if !strings.Contains(string(raw), "9007199254740991") || strings.Contains(string(raw), "18446744073709551615") {
		t.Fatalf("composition did not carry the exact-JSON integer bound: %s", raw)
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Branches []json.RawMessage `json:"oneOf"`
	}
	if err := json.Unmarshal(inputSchema(t, raw), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 0 || len(schema.Branches) != 2 {
		t.Fatal(string(raw))
	}
	for _, input := range []string{`{"input":{"steps":[],"revision":18446744073709551616}}`, `{"input":{"steps":[],"revision":1,"plan_id":"p"}}`} {
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(input)}); err == nil {
			t.Fatal("hints weakened alternatives", input)
		}
	}
	var nilTool *Func[first]
	if _, err := Compose(provider.ToolDefinition{Name: "nil"}, nilTool); err == nil {
		t.Fatal("accepted nil tool")
	}
}

func TestNestedCompositionDoesNotNarrowMixedPropertyTypes(t *testing.T) {
	type textArgs struct {
		Value string `json:"value"`
	}
	type numberArgs struct {
		Value int `json:"value"`
	}
	type boolArgs struct {
		Value bool `json:"value"`
	}
	textTool := Func[textArgs]{Spec: Definition[textArgs]{Name: "text", Parameters: parameters[textArgs]()}, Invoke: func(context.Context, Call, textArgs) (Result, error) { return Result{}, nil }}
	numberTool := Func[numberArgs]{Spec: Definition[numberArgs]{Name: "number", Parameters: parameters[numberArgs]()}, Invoke: func(context.Context, Call, numberArgs) (Result, error) { return Result{}, nil }}
	boolTool := Func[boolArgs]{Spec: Definition[boolArgs]{Name: "bool", Parameters: parameters[boolArgs]()}, Invoke: func(context.Context, Call, boolArgs) (Result, error) { return Result{}, nil }}
	nested, err := Compose(provider.ToolDefinition{Name: "nested"}, textTool, numberTool)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := Compose(provider.ToolDefinition{Name: "outer"}, nested, boolTool)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"input":{"value":"text"}}`, `{"input":{"value":1}}`, `{"input":{"value":true}}`} {
		if _, err := outer.Call(context.Background(), Call{Arguments: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema(t, outer.Definition().Parameters), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["value"]["type"]; ok {
		t.Fatal("outer hint narrowed nested alternatives")
	}
}

func TestRejectionNamesEveryDisallowedFieldWithHints(t *testing.T) {
	params, err := NewParameters[contractArgs](contractConstraints(Reject("steps[]", "note", "notes come from worker progress"),
		Reject("", "workdir", "prefix the command with cd"))...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = params.Decode(json.RawMessage(`{"input":{"title":"p","workdir":"/x","actor":"forged","steps":[{"step_id":"s","note":"n","priority":1,"status":null}],"count":null,"pages":null}}`))
	if err == nil {
		t.Fatal("accepted unknown fields")
	}
	// The first disallowed field keeps its original phrasing; the rest follow.
	want := "arguments.input.actor is not an allowed field (also not allowed: workdir); workdir: prefix the command with cd"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err, want)
	}
	_, err = params.Decode(json.RawMessage(`{"input":{"title":"p","steps":[{"step_id":"s","note":"n","priority":1,"status":null}],"count":null,"pages":null}}`))
	if err == nil || err.Error() != "arguments.input.steps[0].note is not an allowed field (also not allowed: priority); note: notes come from worker progress" {
		t.Fatalf("nested rejection: %v", err)
	}
	_, err = params.Decode(json.RawMessage(`{"input":{"steps":[{"status":"pending"}],"count":null,"pages":null}}`))
	if err == nil || err.Error() != "arguments.input.title is required" {
		t.Fatalf("required phrasing changed: %v", err)
	}
	_, err = params.Decode(json.RawMessage(`{"input":{"title":"p","steps":[{"status":null}],"count":null,"pages":null}}`))
	if err == nil || err.Error() != "arguments.input.steps[0].step_id is required" {
		t.Fatalf("nested required phrasing changed: %v", err)
	}
	for _, bad := range []Constraint{Reject("", "title", "exists"), Reject("steps[]", "", "empty"), Reject("title", "x", "not an object"), Reject("", "x", "")} {
		if _, err := NewParameters[contractArgs](contractConstraints(bad)...); err == nil {
			t.Fatal("accepted invalid reject hint")
		}
	}
}

// Qwen tool parsers hand a nested array through as its raw text when the
// generated value does not parse, so the message must name the trailing text
// rather than report a bare type mismatch. Every other wrong type keeps the
// short phrasing.
func TestStringifiedCompositeExplainsTheTrailingText(t *testing.T) {
	params, err := NewParameters[contractArgs](contractConstraints()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ raw, want string }{
		{`{"input":{"title":"p","steps":"[{\"step_id\":\"s\"}]}","count":null,"pages":null}}`, "arguments.input.steps must be array, not a string; send the array itself and stop at its closing ], with no characters after it"},
		{`{"input":{"title":"p","steps":["{\"step_id\":\"s\"}"],"count":null,"pages":null}}`, "arguments.input.steps[0] must be object, not a string; send the object itself and stop at its closing }, with no characters after it"},
		{`{"input":{"title":"p","steps":5,"count":null,"pages":null}}`, "arguments.input.steps must be array"},
		{`{"input":{"title":"p","steps":[{"step_id":["s"],"status":null}],"count":null,"pages":null}}`, "arguments.input.steps[0].step_id must be string"},
	} {
		_, err := params.Decode(json.RawMessage(c.raw))
		if err == nil || err.Error() != c.want {
			t.Fatalf("%s: got %v, want %q", c.raw, err, c.want)
		}
	}
}

// Models repair exactly what a message names, so a rejection that names only
// the first bad element of an array costs one round trip per element. A single
// failure keeps its unaggregated phrasing.
func TestArrayRejectionNamesEveryFailingElement(t *testing.T) {
	params, err := NewParameters[contractArgs](contractConstraints()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ raw, want string }{
		{`{"input":{"title":"p","steps":[{"step_id":"s","status":null},{"status":null},{"status":null}],"count":null,"pages":null}}`, "arguments.input.steps[1].step_id is required (also: arguments.input.steps[2].step_id is required)"},
		{`{"input":{"title":"p","steps":[{"status":null}],"count":null,"pages":null}}`, "arguments.input.steps[0].step_id is required"},
		{`{"input":{"title":"p","steps":[{"step_id":"s","status":null},{"note":"n","status":null}],"count":null,"pages":null}}`, "arguments.input.steps[1].note is not an allowed field"},
	} {
		_, err := params.Decode(json.RawMessage(c.raw))
		if err == nil || err.Error() != c.want {
			t.Fatalf("%s: got %v, want %q", c.raw, err, c.want)
		}
	}
}
