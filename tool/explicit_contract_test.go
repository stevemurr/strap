package tool

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/work"
	"reflect"
	"slices"
	"testing"
)

func assignmentTool(t *testing.T, name string, h Handler[work.AssignmentRequest]) Tool {
	t.Helper()
	for _, op := range AssignmentTools(h) {
		if op.Definition().Name == name {
			return op
		}
	}
	t.Fatalf("assignment tool %q is missing", name)
	return nil
}

func TestAssignmentWireAndToolDecodersAgree(t *testing.T) {
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"assign_implementation", `{"assignee":"impl","task":"do it"}`, true},
		{"assign_audit", `{"assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub"}`, true},
		{"assign_repair", `{"assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit"}`, true},
		{"assign_research", `{"assignee":"researcher","task":"investigate"}`, true},
		{"assign_implementation", `{"task":"do it"}`, false},
		{"assign_implementation", `{"assignee":null,"task":"do it"}`, false},
		{"assign_implementation", `{"assignee":"","task":"do it"}`, false},
		{"assign_implementation", `{"assignee":" ","task":"do it"}`, false},
		{"assign_implementation", `{"assignee":"impl","task":" "}`, false},
		{"assign_implementation", `{"assignee":"impl","task":"do it","work_id":""}`, false},
		{"assign_implementation", `{"assignee":"impl","task":"do it","audit_id":null}`, false},
		{"assign_audit", `{"assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub","task":""}`, false},
		{"assign_repair", `{"assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit","submission_id":""}`, false},
		{"assign_repair", `{"assignee":"impl","work_id":"work","expected_revision":0,"audit_id":"audit"}`, false},
		{"assign_repair", `{"assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit","scope":null}`, false},
		{"assign_research", `{"assignee":"researcher","task":"investigate","scope":{"plan_id":"p","step_ids":["s"]}}`, false},
		{"assign_audit", `{"kind":"audit","assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.raw, func(t *testing.T) {
			calls := 0
			var got work.AssignmentRequest
			op := assignmentTool(t, tc.name, func(_ context.Context, _ Call, r work.AssignmentRequest) (Result, error) {
				calls++
				got = r
				return Text("ok"), nil
			})
			_, toolErr := op.Call(context.Background(), Call{Arguments: json.RawMessage(tc.raw)})
			wire, wireErr := DecodeAssignment(tc.name, json.RawMessage(tc.raw))
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
	for _, name := range []string{"assign_work", "assign", "unknown"} {
		if _, err := DecodeAssignment(name, json.RawMessage(`{"kind":"implementation","assignee":"impl","task":"do it"}`)); err == nil {
			t.Fatalf("legacy or unknown operation %q accepted", name)
		}
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

func TestAssignmentCatalogAdvertisesOnlyOperationSpecificArguments(t *testing.T) {
	want := map[string][]string{
		"assign_implementation": {"assignee", "context", "expected_output", "scope", "task"},
		"assign_audit":          {"assignee", "expected_revision", "submission_id", "work_id"},
		"assign_repair":         {"assignee", "audit_id", "expected_revision", "work_id"},
		"assign_research":       {"assignee", "context", "expected_output", "task"},
	}
	operations := AssignmentTools(func(context.Context, Call, work.AssignmentRequest) (Result, error) { return Text("ok"), nil })
	if len(operations) != len(want) {
		t.Fatalf("assignment catalog has %d operations, want %d", len(operations), len(want))
	}
	for _, op := range operations {
		def := op.Definition()
		expected, ok := want[def.Name]
		if !ok {
			t.Fatalf("unexpected assignment operation %q", def.Name)
		}
		delete(want, def.Name)
		var schema struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			AdditionalProperties bool                       `json:"additionalProperties"`
		}
		if err := json.Unmarshal(def.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		var fields []string
		for field := range schema.Properties {
			fields = append(fields, field)
		}
		slices.Sort(fields)
		if schema.Type != "object" || schema.AdditionalProperties || !slices.Equal(fields, expected) || def.Description == "" {
			t.Fatalf("%s advertises an unexpected contract: %s", def.Name, def.Parameters)
		}
	}
}
