package tool

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/work"
	"reflect"
	"strings"
	"testing"
)

func TestAssignmentWireAndToolDecodersAgree(t *testing.T) {
	cases := []struct {
		raw   string
		valid bool
	}{
		{`{"kind":"implementation","assignee":"impl","task":"do it"}`, true},
		{`{"kind":"audit","assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub"}`, true},
		{`{"kind":"repair","assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit"}`, true},
		{`{"kind":"implementation","task":"do it"}`, false},
		{`{"kind":"implementation","assignee":null,"task":"do it"}`, false},
		{`{"kind":"implementation","assignee":"","task":"do it"}`, false},
		{`{"kind":"implementation","assignee":" ","task":"do it"}`, false},
		{`{"kind":"implementation","assignee":"impl","task":" "}`, false},
		{`{"kind":"implementation","assignee":"impl","task":"do it","work_id":""}`, false},
		{`{"kind":"implementation","assignee":"impl","task":"do it","audit_id":null}`, true}, // null means omitted
		{`{"kind":"audit","assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub","task":""}`, false},
		{`{"kind":"repair","assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit","submission_id":""}`, false},
		{`{"kind":"repair","assignee":"impl","work_id":"work","expected_revision":0,"audit_id":"audit"}`, false},
		{`{"kind":"repair","assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit","scope":null}`, true}, // null means omitted
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			calls := 0
			var got work.AssignmentRequest
			op := AssignWork(func(_ context.Context, _ Call, r work.AssignmentRequest) (Result, error) {
				calls++
				got = r
				return Text("ok"), nil
			})
			_, toolErr := op.Call(context.Background(), Call{Arguments: json.RawMessage(tc.raw)})
			wire, wireErr := DecodeAssignment(json.RawMessage(tc.raw))
			if (toolErr == nil) != tc.valid || (wireErr == nil) != tc.valid {
				t.Fatal(toolErr, wireErr)
			}
			if tc.valid && (!reflect.DeepEqual(got, wire) || calls != 1) {
				t.Fatal(got, wire, calls)
			}
			if !tc.valid && calls != 0 {
				t.Fatal("invalid request called handler")
			}
		})
	}
}
func TestListWorkContinuationRejectsReplacementFilters(t *testing.T) {
	op := ListWork(func(context.Context, Call, work.ListQuery) (Result, error) { return Text("ok"), nil })
	for _, raw := range []string{`{"cursor":"token","state":""}`, `{"cursor":"token","assignee":"x"}`, `{"limit":0}`, `{"limit":101}`, `{"cursor":""}`} {
		if _, e := op.Call(context.Background(), Call{Arguments: []byte(raw)}); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestAssignmentSchemaDoesNotAdvertiseInternalCallableNames(t *testing.T) {
	op := AssignWork(func(context.Context, Call, work.AssignmentRequest) (Result, error) { return Text("ok"), nil })
	raw := string(op.Definition().Parameters)
	for _, name := range []string{"assign_implementation", "assign_audit", "assign_repair"} {
		if strings.Contains(raw, name) {
			t.Fatalf("schema exposes internal tool name %s", name)
		}
	}
	// The four operations are one flat object keyed by kind, never a oneOf.
	var schema struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			Kind struct {
				Enum []string `json:"enum"`
			} `json:"kind"`
		} `json:"properties"`
	}
	if e := json.Unmarshal([]byte(raw), &schema); e != nil || schema.Type != "object" || len(schema.Properties.Kind.Enum) != 4 || strings.Contains(raw, "oneOf") {
		t.Fatalf("lost variants: %v %+v", e, schema)
	}
	if len(schema.Required) != 2 || schema.Required[0] != "kind" || schema.Required[1] != "assignee" {
		t.Fatalf("required %v", schema.Required)
	}
}
