package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

func TestProgressStrictPayloadCombinations(t *testing.T) {
	calls := 0
	op := ReportWorkProgress(func(_ context.Context, _ Call, a work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("ok"), nil
	})
	schema := compileExportedSchema(t, op.Definition().Parameters)
	position := `{"objective":"verify","activity":null,"note":null,"next_step":null,"uncertainty":null,"blocker":null,"decision_need":null,"dependencies":null}`
	findings := `[{"claim":"observed","basis":"observed","evidence":[{"uri":"exec://test","revision":null,"locator":null,"detail":null}],"limitation":null,"supersedes":null}]`
	steps := `[{"step_id":"s","status":"pending","note":null}]`
	for mask := 0; mask < 8; mask++ {
		values := []string{"null", "null", "null"}
		for i, v := range []string{position, findings, steps} {
			if mask&(1<<i) != 0 {
				values[i] = v
			}
		}
		raw := json.RawMessage(`{"input":{"work_id":"w","position":` + values[0] + `,"findings":` + values[1] + `,"steps":` + values[2] + `}}`)
		se := validateExportedSchema(t, schema, raw)
		before := calls
		_, de := op.Call(context.Background(), Call{Arguments: raw})
		if (se == nil) != (mask != 0) || (de == nil) != (mask != 0) {
			t.Fatalf("mask %d schema=%v call=%v", mask, se, de)
		}
		if mask == 0 && calls != before {
			t.Fatal("all-null call reached handler")
		}
	}
	seed := `{"input":{"work_id":"w","position":` + position + `,"findings":` + findings + `,"steps":null}}`
	for _, m := range []schemaMutation{
		{path: "work_id"}, {path: "work_id", replacement: `null`}, {path: "position"},
		{path: "position.objective"}, {path: "position.objective", replacement: `null`},
		{path: "findings", replacement: `[{"claim":"x","basis":"invalid","evidence":null,"limitation":null,"supersedes":null}]`},
		{path: "findings", replacement: `[{"claim":"x","basis":"observed","evidence":[{"revision":null,"locator":null,"detail":null}],"limitation":null,"supersedes":null}]`},
		{path: "extra", replacement: `null`}, {path: "position.extra", replacement: `null`},
	} {
		raw := mutateSchemaSeed(t, seed, m)
		before := calls
		if err := validateExportedSchema(t, schema, raw); err == nil {
			t.Fatal("schema accepted", string(raw))
		}
		if _, err := op.Call(context.Background(), Call{Arguments: raw}); err == nil || calls != before {
			t.Fatal("invalid call reached handler", string(raw))
		}
	}
}

func TestCompositionWithNonNullGroupUsesOneContract(t *testing.T) {
	type form struct {
		Kind   string   `json:"kind"`
		Text   *string  `json:"text"`
		Values []string `json:"values"`
	}
	calls := 0
	makeForm := func(name string) Tool {
		return builtin(name, "", func(context.Context, Call, form) (Result, error) { calls++; return Text("ok"), nil }, Enum("kind", name), Nullable("text", "no text"), Nullable("values", "no values"), AtLeastOneNonNull("", "text", "values"))
	}
	op, err := ComposeBy("kind", provider.ToolDefinition{Name: "select"}, makeForm("a"), makeForm("b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTool(op); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"input":{"kind":"a","text":"null","values":null}}`, `{"input":{"kind":"b","text":null,"values":[]}}`, `{"input":{"kind":"a","text":"  preserve\n","values":["x"]}}`} {
		if err := validateExportedSchema(t, compileExportedSchema(t, op.Definition().Parameters), []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if err := ValidateArguments(op, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	for _, raw := range []string{`{"input":{"kind":"a","text":null,"values":null}}`, `{"input":{"kind":"a","text":"x","values":null,"kind":"b"}}`} {
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted invalid form", raw)
		}
	}
	if calls != 3 {
		t.Fatal("invalid form invoked handler")
	}
}

func TestContractExpansionIsBounded(t *testing.T) {
	type form struct {
		Value string `json:"value"`
	}
	op := Tool(builtin("leaf", "", func(context.Context, Call, form) (Result, error) { return Result{}, nil }))
	for i := 0; i < 20; i++ {
		other, err := Compose(provider.ToolDefinition{Name: "other"}, op)
		if err != nil {
			t.Fatal(err)
		}
		next, err := Compose(provider.ToolDefinition{Name: "outer"}, op, other)
		if err != nil {
			if !strings.Contains(err.Error(), "schema expansion") {
				t.Fatal(err)
			}
			return
		}
		op = next
	}
	t.Fatal("unbounded repeated composition")
}
