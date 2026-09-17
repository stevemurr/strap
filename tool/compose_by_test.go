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
// keeping the discriminator and common properties visible at the top level.
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
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.OneOf) != 2 || schema.Type != "object" {
		t.Fatalf("lost audit branch constraints: %s", raw)
	}
	if got := schema.Properties["verdict"].Enum; strings.Join(got, ",") != "pass,fail" {
		t.Fatalf("verdict enum %v", got)
	}
	if strings.Join(schema.Required, ",") != "verdict,expected_revision,submission_id,summary,work_id" {
		t.Fatalf("required %v", schema.Required)
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
	if err := call(`{"work_id":"w","expected_revision":1,"submission_id":"s","summary":"ok"}`); err == nil || !strings.Contains(err.Error(), "requires verdict: one of pass, fail") {
		t.Fatalf("missing verdict: %v", err)
	}
	if err := call(`{"verdict":"maybe","work_id":"w","expected_revision":1,"submission_id":"s","summary":"ok"}`); err == nil || !strings.Contains(err.Error(), `verdict must be one of pass, fail, not "maybe"`) {
		t.Fatalf("unknown verdict: %v", err)
	}
	err := call(`{"verdict":"pass","work_id":"w","expected_revision":1,"submission_id":"s","summary":"ok","task":"extra"}`)
	if err == nil || !strings.HasPrefix(err.Error(), "submit_audit with verdict pass: ") || !strings.Contains(err.Error(), "task is not an allowed field") || strings.Contains(err.Error(), "form 1") {
		t.Fatalf("wrong field for the pass form: %v", err)
	}
	if err := call(`{"verdict":"pass","work_id":"w","expected_revision":1,"submission_id":"s","summary":"checked"}`); err != nil || got.Verdict != work.Verdict("pass") || got.Summary != "checked" {
		t.Fatalf("valid audit: %v %+v", err, got)
	}
	if err := call(`{"verdict":"fail","work_id":"w","expected_revision":1,"submission_id":"s","summary":"broken"}`); err == nil || !strings.HasPrefix(err.Error(), "submit_audit with verdict fail: ") || !strings.Contains(err.Error(), "findings") {
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
		Kind string `json:"kind,omitempty"`
	}
	optionalBranch := builtin("optional", "", func(context.Context, Call, optional) (Result, error) { return Text("ok"), nil }, Enum("kind", "optional"))
	if _, err := ComposeBy("kind", provider.ToolDefinition{Name: "t"}, optionalBranch); err == nil {
		t.Fatal("optional discriminator accepted despite required dispatch")
	}
}

// Undiscriminated compositions keep their oneOf but advertise, at the top
// level, the properties every form requires.
func TestCompositionAdvertisesRequiredByEveryForm(t *testing.T) {
	type byID struct {
		ID    string `json:"id"`
		Title string `json:"title,omitempty"`
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
	if err := json.Unmarshal(op.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if strings.Join(schema.Required, ",") != "id" || len(schema.OneOf) != 2 {
		t.Fatalf("required %v oneOf %d", schema.Required, len(schema.OneOf))
	}
}
