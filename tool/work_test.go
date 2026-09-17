package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stevemurr/strap/work"
)

func TestPlanToolRoundTripKeepsPlanAndWorkRevisionsSeparate(t *testing.T) {
	store := work.New()
	plans := func(_ context.Context, c Call, u work.PlanUpdate) (Result, error) {
		p, err := store.UpdatePlan(c.Actor, u)
		if err != nil {
			return Result{}, err
		}
		return JSON(p)
	}
	owner := CreatePlan(plans)
	worker := ReportWorkProgress(func(_ context.Context, c Call, u work.ReportWorkProgressRequest) (Result, error) {
		w, err := store.ReportWorkProgress(c.Actor, u)
		if err != nil {
			return Result{}, err
		}
		return JSON(w)
	})
	created, err := owner.Call(context.Background(), Call{Actor: "root", Arguments: []byte(`{"title":"Plan","steps":[{"title":"Implement"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var p work.Plan
	if err := json.Unmarshal([]byte(created.Content.Text()), &p); err != nil {
		t.Fatal(err)
	}
	w, err := store.AssignWork("root", work.AssignRequest{Assignee: "worker", Task: "Implement", Scope: &work.Scope{PlanID: p.ID, StepIDs: []work.StepID{p.Steps[0].ID}}})
	if err != nil {
		t.Fatal(err)
	}
	args := []byte(fmt.Sprintf(`{"work_id":%q,"expected_revision":%d,"steps":[{"step_id":%q,"status":"ready_for_review"}]}`, w.ID, w.Revision, p.Steps[0].ID))
	updated, err := worker.Call(context.Background(), Call{Actor: "worker", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var current work.ReportWorkProgressResult
	if err := json.Unmarshal([]byte(updated.Content.Text()), &current); err != nil {
		t.Fatal(err)
	}
	if current.WorkRevision != w.Revision+1 {
		t.Fatal("progress result did not expose next revision")
	}
	if _, err := worker.Call(context.Background(), Call{Actor: "worker", Arguments: args}); !errors.Is(err, work.ErrConflict) {
		t.Fatalf("reusing a revision must conflict: %v", err)
	}
	// Progress does not consume the owner's structural plan revision.
	edit := []byte(fmt.Sprintf(`{"plan_id":%q,"expected_revision":%d,"title":"Updated plan"}`, p.ID, p.Revision))
	if _, err := RenamePlan(plans).Call(context.Background(), Call{Actor: "root", Arguments: edit}); err != nil {
		t.Fatal(err)
	}
	submit := SubmitWork(func(_ context.Context, c Call, r work.SubmitRequest) (Result, error) {
		submission, err := store.SubmitWork(c.Actor, r)
		if err != nil {
			return Result{}, err
		}
		return JSON(submission)
	})
	args = []byte(fmt.Sprintf(`{"work_id":%q,"expected_revision":%d,"summary":"Done"}`, current.WorkID, current.WorkRevision))
	if _, err := submit.Call(context.Background(), Call{Actor: "worker", Arguments: args}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitAuditDoesNotAcceptAuthorityFields(t *testing.T) {
	calls := 0
	op := SubmitAudit(func(_ context.Context, c Call, r work.AuditRequest) (Result, error) { calls++; return Text("ok"), nil })
	for _, raw := range []string{`{"work_id":"a","expected_revision":1,"submission_id":"s","verdict":"pass","summary":"ok","assignee":"attacker"}`, `{"work_id":"a","expected_revision":1,"submission_id":"s","verdict":"fail","summary":"bad","findings":[{"description":"x","required_change":"x","verification":"x","owner":"attacker"}]}`} {
		if _, e := op.Call(context.Background(), Call{Arguments: []byte(raw)}); e == nil {
			t.Fatal("accepted authority input")
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestWorkToolSchemasHaveValidRequiredArrays(t *testing.T) {
	definitions := append(PlanTools(func(context.Context, Call, work.PlanUpdate) (Result, error) { return Result{}, nil }),
		SubmitWork(nil), SubmitAudit(nil), GetWork(nil), GetPlan(nil), GetAudit(nil), CancelWork(nil), ReassignWork(nil))
	definitions = append(definitions, AssignmentTools(nil)...)
	var check func(any)
	check = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, item := range v {
				if k == "required" {
					if _, ok := item.([]any); !ok {
						t.Fatalf("required must be an array: %#v", item)
					}
				}
				check(item)
			}
		case []any:
			for _, item := range v {
				check(item)
			}
		}
	}
	for _, op := range definitions {
		var schema any
		if e := json.Unmarshal(op.Definition().Parameters, &schema); e != nil {
			t.Fatal(e)
		}
		check(schema)
	}
}

func TestSubmitAuditFindingsContract(t *testing.T) {
	finding := `[{"description":"missing check","required_change":"add check","verification":"run test"}]`
	for _, tc := range []struct {
		name, verdict, findings string
		valid                   bool
	}{
		{"pass omitted", "pass", "", true},
		{"pass empty", "pass", "[]", true},
		{"pass nonempty", "pass", finding, false},
		{"pass null", "pass", "null", false},
		{"pass string", "pass", `"[]"`, false},
		{"fail omitted", "fail", "", false},
		{"fail empty", "fail", "[]", false},
		{"fail nonempty", "fail", finding, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			op := SubmitAudit(func(_ context.Context, _ Call, request work.AuditRequest) (Result, error) {
				calls++
				if request.Verdict != work.Verdict(tc.verdict) {
					t.Fatal(request)
				}
				if tc.findings == "[]" && request.Findings == nil {
					t.Fatal("explicit empty findings lost in adapter")
				}
				return Text("recorded"), nil
			})
			raw := `{"work_id":"audit-1","expected_revision":1,"submission_id":"submission-1","summary":"checked","verdict":"` + tc.verdict + `"`
			if tc.findings != "" {
				raw += `,"findings":` + tc.findings
			}
			raw += `}`
			_, err := op.Call(context.Background(), Call{Arguments: []byte(raw)})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && calls != 1 || !tc.valid && calls != 0 {
				t.Fatalf("unexpected handler calls: %d", calls)
			}
		})
	}
	// Common fields remain visible at the top level, while oneOf preserves
	// each verdict's complete argument constraints.
	op := SubmitAudit(func(context.Context, Call, work.AuditRequest) (Result, error) { return Result{}, nil })
	var schema struct {
		Type       string            `json:"type"`
		OneOf      []json.RawMessage `json:"oneOf"`
		Required   []string          `json:"required"`
		Properties map[string]struct {
			Enum        []string `json:"enum"`
			Description string   `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(op.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || !slices.Equal(schema.Properties["verdict"].Enum, []string{"pass", "fail"}) || len(schema.OneOf) != 2 {
		t.Fatalf("audit schema shape: %+v", schema)
	}
	if !slices.Equal(schema.Required, []string{"verdict", "expected_revision", "submission_id", "summary", "work_id"}) {
		t.Fatalf("required %v", schema.Required)
	}
	if _, ok := schema.Properties["findings"]; !ok {
		t.Fatal("schema omits findings")
	}
}

func TestCreatePlanExposesOnlyTitleAndCriteriaPerStep(t *testing.T) {
	op := CreatePlan(func(context.Context, Call, work.PlanUpdate) (Result, error) { return Result{}, nil })
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Steps struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"items"`
			} `json:"steps"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(op.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	step := schema.Properties.Steps.Items
	if len(step.Properties) != 2 || step.Properties["title"] == nil || step.Properties["acceptance_criteria"] == nil || len(step.Required) != 1 {
		t.Fatalf("step shape: %v required %v", step.Properties, step.Required)
	}
	if strings.Contains(string(op.Definition().Parameters), "oneOf") {
		t.Fatal("creation must be a single flat form")
	}
}

// Each plan tool is one operation with a flat argument set. Foreign fields are
// rejected before the handler with guidance, and every accepted call maps to
// the single PlanUpdate shape the store understands.
func TestPlanToolsMapToOnePlanUpdateEach(t *testing.T) {
	var got []work.PlanUpdate
	handle := func(_ context.Context, c Call, u work.PlanUpdate) (Result, error) {
		if c.Actor != "root" {
			t.Fatal("identity lost")
		}
		got = append(got, u)
		return Text("ok"), nil
	}
	tools := map[string]Tool{}
	for _, op := range PlanTools(handle) {
		tools[op.Definition().Name] = op
	}
	accepted := []struct{ tool, raw string }{
		{"create_plan", `{"title":"p","steps":[{"title":"s","acceptance_criteria":["a"]},{"title":"t"}]}`},
		{"create_plan", `{"steps":[{"title":"Only step"}]}`},
		{"create_plan", `{"title":"  ","steps":[{"title":"Blank title"}]}`},
		{"add_step", `{"plan_id":"plan-x","expected_revision":3,"title":"new","acceptance_criteria":[]}`},
		{"edit_step", `{"plan_id":"plan-x","expected_revision":3,"step_id":"step-y","title":"renamed"}`},
		{"edit_step", `{"plan_id":"plan-x","expected_revision":3,"step_id":"step-y","acceptance_criteria":["b"]}`},
		{"cancel_steps", `{"plan_id":"plan-x","expected_revision":3,"step_ids":["step-y","step-z"]}`},
		{"reorder_steps", `{"plan_id":"plan-x","expected_revision":3,"order":["step-z","step-y"]}`},
		{"rename_plan", `{"plan_id":"plan-x","expected_revision":3,"title":"Renamed"}`},
	}
	for _, c := range accepted {
		if _, err := tools[c.tool].Call(context.Background(), Call{Actor: "root", Arguments: []byte(c.raw)}); err != nil {
			t.Fatalf("%s %s: %v", c.tool, c.raw, err)
		}
	}
	rev := work.Revision(3)
	id := work.PlanID("plan-x")
	step := work.StepID("step-y")
	want := []work.PlanUpdate{
		{Title: ptr("p"), Steps: []work.StepEdit{{Title: ptr("s"), AcceptanceCriteria: ptr([]string{"a"})}, {Title: ptr("t")}}},
		{Title: ptr("Only step"), Steps: []work.StepEdit{{Title: ptr("Only step")}}},
		{Title: ptr("Blank title"), Steps: []work.StepEdit{{Title: ptr("Blank title")}}},
		{PlanID: &id, ExpectedRevision: &rev, Steps: []work.StepEdit{{Title: ptr("new"), AcceptanceCriteria: ptr([]string{})}}},
		{PlanID: &id, ExpectedRevision: &rev, Steps: []work.StepEdit{{ID: &step, Title: ptr("renamed")}}},
		{PlanID: &id, ExpectedRevision: &rev, Steps: []work.StepEdit{{ID: &step, AcceptanceCriteria: ptr([]string{"b"})}}},
		{PlanID: &id, ExpectedRevision: &rev, Cancel: []work.StepID{"step-y", "step-z"}},
		{PlanID: &id, ExpectedRevision: &rev, Order: []work.StepID{"step-z", "step-y"}},
		{PlanID: &id, ExpectedRevision: &rev, Title: ptr("Renamed")},
	}
	if a, b := mustJSON(t, got), mustJSON(t, want); a != b {
		t.Fatalf("plan updates diverged:\n got %s\nwant %s", a, b)
	}
	rejected := []struct{ tool, raw, want string }{
		{"create_plan", `{"title":"p","steps":[{"title":"s","status":"completed"}]}`, "status: step status is never set through plan tools"},
		{"create_plan", `{"title":"p","steps":[{"title":"s","step_id":"step-1"}]}`, "creation issues step IDs"},
		{"create_plan", `{"plan_id":"plan-x","expected_revision":1,"title":"p","steps":[{"title":"s"}]}`, "create_plan takes no revision"},
		{"create_plan", `{"title":"p","steps":"[{\"title\":\"s\"}]"}`, "steps must be array"},
		{"create_plan", `{"title":"p","steps":[]}`, "requires at least 1 items"},
		{"add_step", `{"plan_id":"plan-x","expected_revision":3,"title":"new","status":"pending"}`, "status: step status is never set through plan tools"},
		{"add_step", `{"plan_id":"plan-x","expected_revision":3,"title":"new","step_id":"step-y"}`, "add_step issues the step_id"},
		{"add_step", `{"plan_id":"plan-x","title":"new"}`, "arguments.expected_revision is required"},
		{"edit_step", `{"plan_id":"plan-x","expected_revision":3,"step_id":"step-y","status":"completed"}`, "status: step status is never set through plan tools"},
		{"edit_step", `{"plan_id":"plan-x","expected_revision":3,"step_id":"step-y"}`, "requires at least one of title, acceptance_criteria"},
		{"edit_step", `{"plan_id":"plan-x","expected_revision":3,"step_id":"step-y","note":"n"}`, "note: step notes come from worker progress reports"},
		{"cancel_steps", `{"plan_id":"plan-x","expected_revision":3,"step_ids":[]}`, "requires at least 1 items"},
		{"reorder_steps", `{"plan_id":"plan-x","expected_revision":"3","order":["a"]}`, "expected_revision must be integer"},
		{"rename_plan", `{"plan_id":"plan-x","expected_revision":3,"title":"","work_id":"w"}`, "work_id is not an allowed field"},
	}
	before := len(got)
	for _, c := range rejected {
		_, err := tools[c.tool].Call(context.Background(), Call{Actor: "root", Arguments: []byte(c.raw)})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s %s: got %v, want %q", c.tool, c.raw, err, c.want)
		}
	}
	if len(got) != before {
		t.Fatal("invalid input reached the handler")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ptr[T any](v T) *T { return &v }

// Revision arguments are copied from receipts, not chosen, so the tools declare
// them as bookkeeping for the agent's repeated-call detection.
func TestWorkToolsDeclareRevisionBookkeeping(t *testing.T) {
	declared := func(tl Tool) []string {
		b, ok := tl.(interface{ BookkeepingParameters() []string })
		if !ok {
			t.Fatalf("%s declares no bookkeeping", tl.Definition().Name)
		}
		return b.BookkeepingParameters()
	}
	plans := func(context.Context, Call, work.PlanUpdate) (Result, error) { return Text("ok"), nil }
	for _, op := range PlanTools(plans)[1:] {
		if got := declared(op); len(got) != 1 || got[0] != "expected_revision" {
			t.Fatalf("%s: %v", op.Definition().Name, got)
		}
	}
	if got := declared(CreatePlan(plans)); len(got) != 0 {
		t.Fatalf("create_plan takes no revision: %v", got)
	}
	for _, op := range AssignmentTools(func(context.Context, Call, work.AssignmentRequest) (Result, error) { return Text("ok"), nil }) {
		got := declared(op)
		if op.Definition().Name == "assign_audit" || op.Definition().Name == "assign_repair" {
			if !slices.Equal(got, []string{"expected_revision"}) {
				t.Fatalf("%s: %v", op.Definition().Name, got)
			}
		} else if len(got) != 0 {
			t.Fatalf("%s has no revision: %v", op.Definition().Name, got)
		}
	}
	if got := declared(ReportWorkProgress(nil)); !slices.Equal(got, []string{"expected_revision"}) {
		t.Fatalf("report_work_progress: %v", got)
	}
}

// The progress tool accepts exactly the advertised shape, without rewriting
// objective aliases, dropping nulls, or re-encoding revision numbers as floats.
func TestProgressReportRejectsUndocumentedAliasesAndNulls(t *testing.T) {
	calls := 0
	report := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("ok"), nil
	})
	for _, raw := range []string{
		`{"work_id":"work-1","expected_revision":1,"assigned_at_revision":1,"objective":"Ship it"}`,
		`{"work_id":"work-1","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"Ship it"},"findings":null}`,
		`{"work_id":"work-1","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"Ship it"},"steps":null}`,
		`{"work_id":"work-1","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"x"},"priority":1}`,
	} {
		if _, err := report.Call(context.Background(), Call{Actor: "worker", Arguments: []byte(raw)}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid progress reports reached handler %d times", calls)
	}
}
