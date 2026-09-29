package tool

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/identity"
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
		{"assign_task", `{"input":{"kind":"implementation","assignee":"impl","task":"do it","context":null,"expected_output":null,"scope":null}}`, true},
		{"assign_audit", `{"input":{"assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub"}}`, true},
		{"assign_repair", `{"input":{"assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit"}}`, true},
		{"assign_task", `{"input":{"kind":"web_research","assignee":"researcher","task":"investigate","context":null,"expected_output":null,"scope":null}}`, true},
		{"assign_task", `{"input":{"kind":"review","assignee":"reviewer","task":"investigate","scope":{"plan_id":"p","step_ids":["s"]},"context":null,"expected_output":null}}`, true},
		{"assign_task", `{"input":{"kind":"experiment","assignee":null,"task":"measure it","context":null,"expected_output":null,"scope":null}}`, true},
		{"assign_task", `{"input":{"kind":"deep_research","assignee":null,"task":"survey it","context":null,"expected_output":null,"scope":null}}`, true},
		{"assign_task", `{"input":{"assignee":"impl","task":"do it","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"research","assignee":"impl","task":"do it","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"repair","assignee":"impl","task":"do it","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"implementation","task":"do it"}}`, false},
		{"assign_task", `{"input":{"kind":"implementation","assignee":"","task":"do it","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"implementation","assignee":" ","task":"do it","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"implementation","assignee":"impl","task":" ","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"implementation","assignee":"impl","task":"do it","work_id":"","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"implementation","assignee":"impl","task":"do it","audit_id":null,"context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_audit", `{"input":{"assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub","task":"","context":null,"expected_output":null,"scope":null}}`, false},
		{"assign_repair", `{"input":{"assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit","submission_id":""}}`, false},
		{"assign_repair", `{"input":{"assignee":"impl","work_id":"work","expected_revision":0,"audit_id":"audit"}}`, false},
		{"assign_repair", `{"input":{"assignee":"impl","work_id":"work","expected_revision":2,"audit_id":"audit","scope":null}}`, false},
		{"assign_task", `{"input":{"kind":"review","assignee":"researcher","task":"investigate","scope":{"plan_id":"p","step_ids":[]},"context":null,"expected_output":null}}`, false},
		{"assign_audit", `{"input":{"kind":"audit","assignee":"auditor","work_id":"work","expected_revision":2,"submission_id":"sub"}}`, false},
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
	for _, name := range []string{"assign_work", "assign", "assign_implementation", "assign_research", "assign_experiment", "unknown"} {
		if _, err := DecodeAssignment(name, json.RawMessage(`{"input":{"kind":"implementation","assignee":"impl","task":"do it","context":null,"expected_output":null,"scope":null}}`)); err == nil {
			t.Fatalf("legacy or unknown operation %q accepted", name)
		}
	}
}

// A null assignee names the role that does the kind, which staffs the work
// with a new agent of that role.
func TestAssignTaskNullAssigneeNamesTheKindsRole(t *testing.T) {
	for kind, role := range map[string]identity.ActorID{"implementation": "implementor", "review": "reviewer", "web_research": "web_researcher", "deep_research": "deep_researcher", "experiment": "experimenter"} {
		r, err := DecodeAssignment("assign_task", json.RawMessage(`{"input":{"kind":"`+kind+`","assignee":null,"task":"t","context":null,"expected_output":null,"scope":null}}`))
		if err != nil || r.Assignee != role || r.Kind != work.Kind(kind) {
			t.Fatal(kind, r, err)
		}
	}
}

func TestListWorkContinuationRejectsReplacementFilters(t *testing.T) {
	op := ListWork(func(context.Context, Call, work.ListQuery) (Result, error) { return Text("ok"), nil })
	for _, raw := range []string{`{"input":{"cursor":"token","state":""}}`, `{"input":{"cursor":"token","assignee":"x"}}`, `{"limit":0}`, `{"limit":101}`, `{"input":{"cursor":""}}`} {
		if _, e := op.Call(context.Background(), Call{Arguments: []byte(raw)}); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestAssignmentCatalogAdvertisesOnlyOperationSpecificArguments(t *testing.T) {
	want := map[string][]string{
		"assign_task":   {"assignee", "context", "expected_output", "kind", "scope", "task"},
		"assign_audit":  {"assignee", "expected_revision", "submission_id", "work_id"},
		"assign_repair": {"assignee", "audit_id", "expected_revision", "work_id"},
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
		if err := json.Unmarshal(inputSchema(t, def.Parameters), &schema); err != nil {
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

// update_todos takes the whole list; each entry needs content and a known
// status.
func TestUpdateTodosValidatesEntries(t *testing.T) {
	calls := 0
	op := UpdateTodos(func(context.Context, Call, UpdateTodosArgs) (Result, error) { calls++; return Text("ok"), nil })
	for raw, valid := range map[string]bool{
		`{"input":{"todos":[{"content":"a","status":"pending"},{"content":"b","status":"completed"}]}}`: true,
		`{"input":{"todos":[]}}`:                                  true,
		`{"input":{"todos":[{"content":"a","status":"done"}]}}`:   false,
		`{"input":{"todos":[{"content":"","status":"pending"}]}}`: false,
		`{"input":{"todos":[{"content":"a"}]}}`:                   false,
	} {
		before := calls
		_, err := op.Call(context.Background(), Call{Arguments: json.RawMessage(raw)})
		if (err == nil) != valid || (calls > before) != valid {
			t.Fatal(raw, err)
		}
	}
}
