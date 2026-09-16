package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

// A discriminated composition advertises one flat object: the discriminator
// as a required enum, every branch property with a note about which values
// accept it, and no oneOf for a tool parser to flatten or drop.
func TestDiscriminatedSchemaIsOneFlatObject(t *testing.T) {
	op := AssignWork(func(context.Context, Call, work.AssignmentRequest) (Result, error) { return Text("ok"), nil })
	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum        []string `json:"enum"`
			Description string   `json:"description"`
		} `json:"properties"`
	}
	raw := op.Definition().Parameters
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "oneOf") || schema.Type != "object" {
		t.Fatalf("not a flat object: %s", raw)
	}
	if got := schema.Properties["kind"].Enum; strings.Join(got, ",") != "implementation,audit,repair,research" {
		t.Fatalf("kind enum %v", got)
	}
	if strings.Join(schema.Required, ",") != "kind,assignee" {
		t.Fatalf("required %v", schema.Required)
	}
	for field, want := range map[string]string{
		"work_id":       "Only when kind is audit or repair",
		"audit_id":      "Only when kind is repair",
		"submission_id": "Only when kind is audit",
		"scope":         "Only when kind is implementation",
		"assignee":      "",
	} {
		if got := schema.Properties[field].Description; !strings.Contains(got, want) {
			t.Fatalf("%s: %q lacks %q", field, got, want)
		}
	}
}

// Dispatch reads the discriminator and reports one form's rule: a missing or
// unknown value names the allowed values, and a wrong field names the form.
func TestDiscriminatedDispatchReportsOneForm(t *testing.T) {
	var got work.AssignmentRequest
	op := AssignWork(func(_ context.Context, _ Call, r work.AssignmentRequest) (Result, error) {
		got = r
		return Text("ok"), nil
	})
	call := func(raw string) error {
		_, err := op.Call(context.Background(), Call{Actor: "root", Arguments: []byte(raw)})
		return err
	}
	if err := call(`{"assignee":"agent-2","task":"implement it","context":"c","expected_output":"e"}`); err == nil || !strings.Contains(err.Error(), "requires kind: one of implementation, audit, repair, research") {
		t.Fatalf("missing kind: %v", err)
	}
	if err := call(`{"kind":"review","assignee":"agent-2"}`); err == nil || !strings.Contains(err.Error(), `kind must be one of implementation, audit, repair, research, not "review"`) {
		t.Fatalf("unknown kind: %v", err)
	}
	err := call(`{"kind":"audit","assignee":"agent-3","work_id":"w","expected_revision":2,"submission_id":"s","task":"extra"}`)
	if err == nil || !strings.HasPrefix(err.Error(), "assign_work with kind audit: ") || !strings.Contains(err.Error(), "task is not an allowed field") || strings.Contains(err.Error(), "form 1") {
		t.Fatalf("wrong field for the audit form: %v", err)
	}
	if err := call(`{"kind":"implementation","assignee":"agent-2","task":"implement it"}`); err != nil || got.Kind != work.Implementation || got.Assignee != "agent-2" {
		t.Fatalf("valid implementation: %v %+v", err, got)
	}
	audit := SubmitAudit(func(context.Context, Call, work.AuditRequest) (Result, error) { return Text("ok"), nil })
	_, err = audit.Call(context.Background(), Call{Actor: "auditor", Arguments: []byte(`{"verdict":"fail","work_id":"w","expected_revision":1,"submission_id":"s","summary":"broken"}`)})
	if err == nil || !strings.HasPrefix(err.Error(), "submit_audit with verdict fail: ") || !strings.Contains(err.Error(), "findings") {
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
