package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// A discriminated composition retains every branch constraint in oneOf while
// placing complete alternatives inside the sole input parameter.
func TestDiscriminatedSchemaRetainsExactBranches(t *testing.T) {
	op := SubmitAudit(func(context.Context, Call, work.AuditRequest) (Result, error) { return Text("ok"), nil })
	var schema struct {
		Type       string            `json:"type"`
		Required   []string          `json:"required"`
		OneOf      []json.RawMessage `json:"oneOf"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	raw := op.Definition().Parameters
	if err := json.Unmarshal(inputSchema(t, raw), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.OneOf) != 2 || schema.Type != "" {
		t.Fatalf("lost audit branch constraints: %s", raw)
	}
	if len(schema.Properties) != 0 || len(schema.Required) != 0 {
		t.Fatal("root hints survived", string(raw))
	}
	for _, branch := range schema.OneOf {
		compileExportedSchema(t, branch)
	}

}

// Dispatch reads the discriminator and reports one form's rule: a missing or
// unknown value names the allowed values, and a wrong field names the form.
func TestDiscriminatedDispatchReportsOneForm(t *testing.T) {
	var got work.AuditRequest
	op := SubmitAudit(func(_ context.Context, _ Call, r work.AuditRequest) (Result, error) {
		got = r
		return Text("ok"), nil
	})
	call := func(raw string) error {
		_, err := op.Call(context.Background(), Call{Actor: "auditor", Arguments: []byte(raw)})
		return err
	}
	if err := call(`{"input":{"work_id":"w","expected_revision":1,"submission_id":"s","summary":"ok"}}`); err == nil || !strings.Contains(err.Error(), "requires input.verdict: one of pass, fail") {
		t.Fatalf("missing verdict: %v", err)
	}
	if err := call(`{"input":{"verdict":"maybe","work_id":"w","expected_revision":1,"submission_id":"s","summary":"ok","findings":null}}`); err == nil || !strings.Contains(err.Error(), `verdict must be one of pass, fail, not "maybe"`) {
		t.Fatalf("unknown verdict: %v", err)
	}
	err := call(`{"input":{"verdict":"pass","work_id":"w","expected_revision":1,"submission_id":"s","summary":"ok","task":"extra","findings":null}}`)
	if err == nil || !strings.HasPrefix(err.Error(), "submit_audit with verdict pass: ") || !strings.Contains(err.Error(), "task is not an allowed field") || strings.Contains(err.Error(), "form 1") {
		t.Fatalf("wrong field for the pass form: %v", err)
	}
	if err := call(`{"input":{"verdict":"pass","work_id":"w","expected_revision":1,"submission_id":"s","summary":"checked","findings":null}}`); err != nil || got.Verdict != work.Verdict("pass") || got.Summary != "checked" {
		t.Fatalf("valid audit: %v %+v", err, got)
	}
	if err := call(`{"input":{"verdict":"fail","work_id":"w","expected_revision":1,"submission_id":"s","summary":"broken","findings":null}}`); err == nil || !strings.HasPrefix(err.Error(), "submit_audit with verdict fail: ") || !strings.Contains(err.Error(), "findings") {
		t.Fatalf("fail without findings: %v", err)
	}
}

// Every branch of a discriminated composition must pin the discriminator to
// one value, and the values must differ.
func TestDiscriminatedCompositionRejectsUnpinnedBranches(t *testing.T) {
	type a struct {
		Kind string `json:"kind"`
	}
	free := builtin("free", "", func(context.Context, Call, a) (Result, error) { return Text("ok"), nil })
	one := builtin("one", "", func(context.Context, Call, a) (Result, error) { return Text("ok"), nil }, Enum("kind", "x"))
	same := builtin("same", "", func(context.Context, Call, a) (Result, error) { return Text("ok"), nil }, Enum("kind", "x"))
	if _, err := ComposeBy("kind", provider.ToolDefinition{Name: "t"}, free, one); err == nil || !strings.Contains(err.Error(), "exactly one enum value") {
		t.Fatal(err)
	}
	if _, err := ComposeBy("kind", provider.ToolDefinition{Name: "t"}, one, same); err == nil || !strings.Contains(err.Error(), "share kind value") {
		t.Fatal(err)
	}
	if _, err := ComposeBy("", provider.ToolDefinition{Name: "t"}, one); err == nil {
		t.Fatal("unnamed discriminator accepted")
	}
	type optional struct {
		Kind *string `json:"kind"`
	}
	optionalBranch := builtin("optional", "", func(context.Context, Call, optional) (Result, error) { return Text("ok"), nil }, Enum("kind", "optional"), Nullable("kind", "no selection"))
	if _, err := ComposeBy("kind", provider.ToolDefinition{Name: "t"}, optionalBranch); err == nil {
		t.Fatal("optional discriminator accepted despite required dispatch")
	}
}

// Undiscriminated compositions keep each complete form inside input.
func TestCompositionKeepsRequiredInsideEachForm(t *testing.T) {
	type byID struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	type byIDNote struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	}
	first := builtin("first", "First form.", func(context.Context, Call, byID) (Result, error) { return Text("ok"), nil }, MinLength("title", 1))
	second := builtin("second", "Second form.", func(context.Context, Call, byIDNote) (Result, error) { return Text("ok"), nil })
	op, err := Compose(provider.ToolDefinition{Name: "t"}, first, second)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string          `json:"required"`
		OneOf    []json.RawMessage `json:"oneOf"`
	}
	if err := json.Unmarshal(inputSchema(t, op.Definition().Parameters), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 0 || len(schema.OneOf) != 2 {
		t.Fatalf("required %v oneOf %d", schema.Required, len(schema.OneOf))
	}
}
