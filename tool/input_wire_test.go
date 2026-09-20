package tool

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// Check the wire envelope before inspecting its logical schema in core tests.
func inputSchema(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional bool                       `json:"additionalProperties"`
		AnyOf      json.RawMessage            `json:"anyOf"`
		OneOf      json.RawMessage            `json:"oneOf"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || len(schema.Properties) != 1 || schema.Properties["input"] == nil || !reflect.DeepEqual(schema.Required, []string{"input"}) || schema.Additional || string(keys["additionalProperties"]) != "false" || schema.AnyOf != nil || schema.OneOf != nil {
		t.Fatalf("not the canonical input envelope: %s", raw)
	}
	return schema.Properties["input"]
}

func TestInputWireEmptyTool(t *testing.T) {
	p := parameters[struct{}]()
	inputSchema(t, p.Schema())
	schema := compileExportedSchema(t, p.Schema())
	for _, raw := range []string{`{}`, `{"input":null}`, `{"input":[]}`, `{"input":{"extra":null}}`, `{"input":{},"extra":null}`} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		if err := validateExportedSchema(t, schema, []byte(raw)); err == nil {
			t.Fatalf("schema accepted %s", raw)
		}
	}
	raw, err := MarshalInput(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"input":{}}` {
		t.Fatal(string(raw))
	}
	if _, err = p.Decode(raw); err != nil {
		t.Fatal(err)
	}
	if err = validateExportedSchema(t, schema, raw); err != nil {
		t.Fatal(err)
	}
}

func TestInputWireValuesAndRejections(t *testing.T) {
	type args struct {
		ID   string  `json:"id"`
		Note *string `json:"note"`
	}
	p := parameters[args](MinLength("id", 1), Nullable("note", "no note"))
	inputSchema(t, p.Schema())
	schema := compileExportedSchema(t, p.Schema())
	for _, note := range []string{"null", "", "  preserve\n", "</parameter></function>", "quote\" slash\\ 雪"} {
		want := args{ID: "w", Note: &note}
		raw, err := MarshalInput(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Decode(raw)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: got %#v, %v", note, got, err)
		}
		if err = validateExportedSchema(t, schema, raw); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.Decode([]byte(`{"input":{"id":"w","note":null}}`))
	if err != nil || got.Note != nil {
		t.Fatal(got, err)
	}
	for _, raw := range []string{`{"id":"w","note":null}`, `{}`, `{"input":null}`, `{"input":"{}"}`, `{"input":{"input":{"id":"w","note":null}}}`, `{"input":{"id":"w"}}`, `{"input":{"id":null,"note":null}}`, `{"input":{"id":"w","note":false}}`, `{"input":{"id":"w","note":null,"extra":1}}`, `{"input":{"id":"w","note":null},"extra":1}`} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		if err := validateExportedSchema(t, schema, []byte(raw)); err == nil {
			t.Fatalf("schema accepted %s", raw)
		}
	}
	// Duplicates are a raw-input rule; a JSON Schema engine may collapse them.
	for _, raw := range []string{`{"input":{"id":"a","id":"b","note":null}}`, `{"input":{"id":"a","note":null},"input":{"id":"b","note":null}}`} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatalf("accepted duplicate %s", raw)
		}
	}
	_, err = p.Decode([]byte(`{"input":{"id":"w"}}`))
	if err == nil || !strings.Contains(err.Error(), "arguments.input.note") {
		t.Fatal(err)
	}
}

func TestInputWireSerializerDoesNotRepair(t *testing.T) {
	raw, err := MarshalInput(map[string]string{"id": "w"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"input":{"id":"w"}}` {
		t.Fatal(string(raw))
	}
	type args struct {
		ID   string  `json:"id"`
		Note *string `json:"note"`
	}
	if _, err := parameters[args](Nullable("note", "no note")).Decode(raw); err == nil {
		t.Fatal("serializer filled missing note")
	}
	if _, err := MarshalInput(make(chan int)); err == nil {
		t.Fatal("unsupported value serialized")
	}
}

func TestInputWireProgressPreservesValues(t *testing.T) {
	note := "  null\n"
	raw, err := MarshalInput(ReportWorkProgressInput{WorkID: "w", Position: &WorkPositionInput{Objective: "verify", Note: &note}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeProgressReport(raw)
	if err != nil || got.Position == nil || got.Position.Note != note {
		t.Fatal(got, err)
	}
	calls := 0
	op := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("ok"), nil
	})
	if err := ValidateTool(op); err != nil {
		t.Fatal(err)
	}
	if err := ValidateArguments(op, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Call(context.Background(), Call{Arguments: raw}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestInputWireCompositionBoundaries(t *testing.T) {
	type args struct {
		Kind  string `json:"kind"`
		Count int    `json:"count"`
	}
	for _, mode := range []string{"plain", "discriminated", "nested"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			branch := func(kind string) Tool {
				return builtin(kind, "", func(context.Context, Call, args) (Result, error) { calls++; return Text("ok"), nil }, Enum("kind", kind), Minimum("count", 1))
			}
			var op Tool
			var err error
			if mode == "discriminated" {
				op, err = ComposeBy("kind", provider.ToolDefinition{Name: "select"}, branch("a"), branch("b"))
			} else {
				op, err = Compose(provider.ToolDefinition{Name: "select"}, branch("a"), branch("b"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "nested" {
				op, err = Compose(provider.ToolDefinition{Name: "outer"}, op, branch("c"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateTool(op); err != nil {
				t.Fatal(err)
			}
			inputSchema(t, op.Definition().Parameters)
			schema := compileExportedSchema(t, op.Definition().Parameters)
			for _, raw := range []string{`{"input":{"kind":"a","count":1}}`, `{"input":{"count":2,"kind":"b"}}`} {
				if err := validateExportedSchema(t, schema, []byte(raw)); err != nil {
					t.Fatal(err)
				}
				if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err != nil {
					t.Fatal(err)
				}
			}
			for _, raw := range []string{`{}`, `{"kind":"a","count":1}`, `{"input":null}`, `{"input":[]}`, `{"input":{"kind":"a","count":1},"extra":null}`, `{"input":{"input":{"kind":"a","count":1}}}`, `{"input":{"count":1}}`, `{"input":{"kind":"z","count":1}}`, `{"input":{"kind":"a","count":0}}`, `{"input":{"kind":"a","count":"1"}}`, `{"input":{"kind":"a","count":1,"extra":null}}`} {
				if err := validateExportedSchema(t, schema, []byte(raw)); err == nil {
					t.Fatalf("schema accepted %s", raw)
				}
				if err := ValidateArguments(op, []byte(raw)); err == nil {
					t.Fatalf("dispatch validation accepted %s", raw)
				}
				if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
					t.Fatalf("call accepted %s", raw)
				}
			}
			if calls != 2 {
				t.Fatalf("invalid input reached handler: %d calls", calls)
			}
		})
	}
}
