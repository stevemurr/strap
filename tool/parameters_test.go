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
	Status *string `json:"status,omitempty"`
}
type contractArgs struct {
	Title string         `json:"title"`
	Count *int8          `json:"count,omitempty"`
	Steps []contractStep `json:"steps,omitempty"`
	Pages []int          `json:"pages,omitempty"`
}

func TestParametersValidateAndDecodeOneContract(t *testing.T) {
	choices := []string{"pending", "ready"}
	params, err := NewParameters[contractArgs](Minimum("count", 1), Maximum("count", 10), MinItems("steps", 1), MinLength("steps[].step_id", 1), Enum("steps[].status", choices...), MinItems("pages", 1), MaxItems("pages", 2), UniqueItems("pages"), Minimum("pages[]", 1))
	if err != nil {
		t.Fatal(err)
	}
	choices[0] = "mutated"
	for _, raw := range []string{`{"title":""}`, `{"title":"plan","count":1.0}`, `{"title":"plan","count":1e1,"steps":[{"step_id":"s","status":"pending"}],"pages":[2,1]}`} {
		got, err := params.Decode(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got.Title == "" && got.Count != nil {
			t.Fatal("omission changed")
		}
	}
	for _, raw := range []string{`{}`, `{"title":null}`, `{"title":"p","Title":"bypass"}`, `{"title":"p","title":"duplicate"}`, `{"title":"p","count":0}`, `{"title":"p","count":11}`, `{"title":"p","count":128}`, `{"title":"p","count":1.5}`, `{"title":"p","steps":"[]"}`, `{"title":"p","steps":[]}`, `{"title":"p","steps":[{}]}`, `{"title":"p","steps":[{"step_id":"s","note":"escape"}]}`, `{"title":"p","steps":[{"step_id":"s","status":"completed"}]}`, `{"title":"p","steps":[{"step_id":"s","status":"done"}]}`, `{"title":"p","pages":[1,1.0]}`, `{"title":"p","pages":[1,2,3]}`, `{"title":"p","pages":[]}`, `{"title":"p"} {}`} {
		if _, err := params.Decode(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(params.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	count := props["count"].(map[string]any)
	if count["minimum"] != float64(1) || count["maximum"] != float64(10) {
		t.Fatal(count)
	}
	if got := schema["required"].([]any); len(got) != 1 || got[0] != "title" {
		t.Fatal(got)
	}
	original := string(params.Schema())
	raw := params.Schema()
	raw[0] = 'x'
	if string(params.Schema()) != original {
		t.Fatal("schema storage leaked")
	}
	if _, err := params.Decode([]byte(`{"title":"p","count":11}`)); err == nil {
		t.Fatal("export changed validation")
	}
}

type recursiveArgs struct {
	Next *recursiveArgs `json:"next,omitempty"`
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
		func() error { _, err := NewParameters[contractArgs](Minimum("missing", 1)); return err },
		func() error { _, err := NewParameters[contractArgs](Minimum("title", 1)); return err },
		func() error { _, err := NewParameters[contractArgs](Maximum("count", 128)); return err },
		func() error {
			_, err := NewParameters[contractArgs](Minimum("count", 10), Maximum("count", 1))
			return err
		},
		func() error {
			_, err := NewParameters[contractArgs](MinItems("steps", 3), MaxItems("steps", 1))
			return err
		},
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("accepted invalid definition %d", i)
		}
	}
	var zero Parameters[contractArgs]
	if _, err := zero.Decode([]byte(`{"title":"p"}`)); err == nil {
		t.Fatal("accepted uninitialized contract")
	}
}
func TestIntegerContractPreservesExactValues(t *testing.T) {
	type args struct {
		Signed   int64  `json:"signed"`
		Unsigned uint64 `json:"unsigned"`
	}
	p := parameters[args]()
	for _, raw := range []string{
		`{"signed":-9223372036854775808,"unsigned":18446744073709551615}`,
		`{"signed":-9223372036854775808.0,"unsigned":184467440737095516150e-1}`,
		`{"signed":0e99999999999999999999,"unsigned":0e-99999999999999999999}`,
	} {
		if _, err := p.Decode([]byte(raw)); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"signed":9223372036854775808,"unsigned":0}`, `{"signed":0,"unsigned":18446744073709551616}`, `{"signed":0,"unsigned":-1}`, `{"signed":1e99999999999999,"unsigned":0}`, `{"signed":1e-99999999999999,"unsigned":0}`} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestFuncRejectsInvalidCallsBeforeHandler(t *testing.T) {
	calls := 0
	f := Func[contractArgs]{Spec: Definition[contractArgs]{Name: "example", Parameters: parameters[contractArgs]()}, Invoke: func(_ context.Context, c Call, a contractArgs) (Result, error) {
		calls++
		if c.Actor != "root" || a.Title != "ok" {
			t.Fatal(c, a)
		}
		return Text("done"), nil
	}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call(context.Background(), Call{Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("missing field accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Call(ctx, Call{Arguments: []byte(`{"title":"ok"}`)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("invalid call reached handler")
	}
	if _, err := f.Call(context.Background(), Call{Actor: "root", Arguments: []byte(`{"title":"ok"}`)}); err != nil {
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
	for raw, want := range map[string]string{`{"title":"p"}`: "create", `{"plan_id":"p"}`: "edit"} {
		got, err := composed.Call(context.Background(), Call{Arguments: []byte(raw)})
		if err != nil || got.Content.Text() != want {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{`{}`, `{"title":"p","plan_id":"p"}`, `{"title":1}`} {
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
	if _, err := ambiguous.Call(context.Background(), Call{Arguments: []byte(`{"plan_id":"p"}`)}); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("ambiguous call executed")
	}
	var schema map[string]any
	if err := json.Unmarshal(composed.Definition().Parameters, &schema); err != nil {
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
	p := parameters[contractArgs]()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if _, err := p.Decode([]byte(`{"title":"p"}`)); err != nil {
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
func TestCompositionPreservesIntegerBoundsAndTopLevelTypeHints(t *testing.T) {
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
	if !strings.Contains(string(raw), "18446744073709551615") {
		t.Fatalf("integer bound rounded in schema: %s", raw)
	}
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Branches []json.RawMessage `json:"oneOf"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["steps"].Type != "array" || schema.Properties["revision"].Type != "integer" || len(schema.Branches) != 2 {
		t.Fatal(string(raw))
	}
	for _, input := range []string{`{"steps":[],"revision":18446744073709551616}`, `{"steps":[],"revision":1,"plan_id":"p"}`} {
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
	for _, raw := range []string{`{"value":"text"}`, `{"value":1}`, `{"value":true}`} {
		if _, err := outer.Call(context.Background(), Call{Arguments: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(outer.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["value"]["type"]; ok {
		t.Fatal("outer hint narrowed nested alternatives")
	}
}

func TestRejectionNamesEveryDisallowedFieldWithHints(t *testing.T) {
	params, err := NewParameters[contractArgs](
		Reject("steps[]", "note", "notes come from worker progress"),
		Reject("", "workdir", "prefix the command with cd"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = params.Decode(json.RawMessage(`{"title":"p","workdir":"/x","actor":"forged","steps":[{"step_id":"s","note":"n","priority":1}]}`))
	if err == nil {
		t.Fatal("accepted unknown fields")
	}
	// The first disallowed field keeps its original phrasing; the rest follow.
	want := "arguments.actor is not an allowed field (also not allowed: workdir); workdir: prefix the command with cd"
	if err.Error() != want {
		t.Fatalf("got %q, want %q", err, want)
	}
	_, err = params.Decode(json.RawMessage(`{"title":"p","steps":[{"step_id":"s","note":"n","priority":1}]}`))
	if err == nil || err.Error() != "arguments.steps[0].note is not an allowed field (also not allowed: priority); note: notes come from worker progress" {
		t.Fatalf("nested rejection: %v", err)
	}
	_, err = params.Decode(json.RawMessage(`{"steps":[{"status":"pending"}]}`))
	if err == nil || err.Error() != "arguments.title is required" {
		t.Fatalf("required phrasing changed: %v", err)
	}
	_, err = params.Decode(json.RawMessage(`{"title":"p","steps":[{}]}`))
	if err == nil || err.Error() != "arguments.steps[0].step_id is required" {
		t.Fatalf("nested required phrasing changed: %v", err)
	}
	for _, bad := range []Constraint{Reject("", "title", "exists"), Reject("steps[]", "", "empty"), Reject("title", "x", "not an object"), Reject("", "x", "")} {
		if _, err := NewParameters[contractArgs](bad); err == nil {
			t.Fatal("accepted invalid reject hint")
		}
	}
}
