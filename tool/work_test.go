package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stevemurr/strap/work"
)

func TestPlanCompositionExposesNestedStepShapes(t *testing.T) {
	op := UpdatePlan(func(context.Context, Call, work.PlanUpdate) (Result, error) { return Result{}, nil },
		func(context.Context, Call, work.ProgressUpdate) (Result, error) { return Result{}, nil })
	var schema struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Items struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(op.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	step := schema.Properties["steps"].Items
	if step.Type != "object" || len(step.Required) != 0 {
		t.Fatal("step hints must expose an object without merging branch requirements")
	}
	for _, field := range []string{"step_id", "title", "acceptance_criteria", "status", "note"} {
		if len(step.Properties[field]) == 0 {
			t.Fatalf("top-level steps.items hides %s", field)
		}
	}
	var criteria struct {
		Type  string `json:"type"`
		Items struct {
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(step.Properties["acceptance_criteria"], &criteria); err != nil {
		t.Fatal(err)
	}
	if criteria.Type != "array" || criteria.Items.Type != "string" {
		t.Fatal("nested criteria array lost its element type")
	}
}

func TestPlanToolRoundTripKeepsPlanAndWorkRevisionsSeparate(t *testing.T) {
	store := work.New()
	owner := UpdatePlan(func(_ context.Context, c Call, u work.PlanUpdate) (Result, error) {
		p, err := store.UpdatePlan(c.Actor, u)
		if err != nil {
			return Result{}, err
		}
		return JSON(p)
	}, nil)
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
	args := []byte(fmt.Sprintf(`{"work_id":%q,"expected_revision":%d,"assigned_at_revision":1,"steps":[{"step_id":%q,"status":"ready_for_review"}]}`, w.ID, w.Revision, p.Steps[0].ID))
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
	if _, err := owner.Call(context.Background(), Call{Actor: "root", Arguments: edit}); err != nil {
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

func TestUpdatePlanSelectorsAndStrictPatches(t *testing.T) {
	plans, progress := 0, 0
	op := UpdatePlan(func(_ context.Context, c Call, u work.PlanUpdate) (Result, error) {
		if c.Actor != "root" {
			t.Fatal("identity lost")
		}
		plans++
		return Text("plan"), nil
	}, func(_ context.Context, c Call, u work.ProgressUpdate) (Result, error) {
		progress++
		return Text("progress"), nil
	})
	for _, raw := range []string{`{"title":"p","steps":[{"title":"s"}]}`, `{"plan_id":"p","expected_revision":1,"title":"new"}`} {
		if _, e := op.Call(context.Background(), Call{Actor: "root", Arguments: []byte(raw)}); e != nil {
			t.Fatal(e)
		}
	}
	if plans != 2 || progress != 0 {
		t.Fatal(plans, progress)
	}
	for _, raw := range []string{`{"plan_id":"p","work_id":"w","expected_revision":1}`, `{"plan_id":null}`, `{"work_id":"w","expected_revision":1,"steps":[{"step_id":"s","title":"escape"}]}`, `{"title":"p","steps":[{"step_id":null,"title":"x"}]}`, `{"title":"p","steps":[{"title":"x","title":"y"}]}`, `{"work_id":"w","steps":[]}`, `{"plan_id":"p","expected_revision":1,"steps":[{"step_id":"s","status":"completed"}]}`} {
		if _, e := op.Call(context.Background(), Call{Actor: "root", Arguments: []byte(raw)}); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	if plans != 2 || progress != 0 {
		t.Fatal("invalid input reached callback")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := op.Call(ctx, Call{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
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
	definitions := []Tool{
		UpdatePlan(func(context.Context, Call, work.PlanUpdate) (Result, error) { return Result{}, nil }, nil), AssignWork(nil), SubmitWork(nil), SubmitAudit(nil), GetWork(nil), GetPlan(nil), GetAudit(nil), CancelWork(nil), ReassignWork(nil),
	}
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

func TestPlanCreationRegressionAndCapabilitySchemas(t *testing.T) {
	calls := 0
	owner := UpdatePlan(func(_ context.Context, _ Call, a work.PlanUpdate) (Result, error) {
		calls++
		if a.PlanID != nil || a.Title == nil || len(a.Steps) != 1 || a.Steps[0].ID != nil {
			t.Fatal(a)
		}
		return Text("created"), nil
	}, nil)
	for _, raw := range []string{
		`{"title":"p","steps":"[{\"title\":\"s\"}]"}`,
		`{"title":"p","steps":[{"title":"s"}],"note":"initial"}`,
		`{"title":"p","steps":[{"title":"s","status":"pending"}]}`,
		`{"steps":[{"title":"s"}]}`,
		`{"title":"p","steps":[{"step_id":"invented","title":"s"}]}`,
		`{"title":"p","steps":[]}`,
		`{"title":"p","steps":[{}]}`,
		`{"title":"p","steps":[{"title":"s"}],"expected_revision":1}`,
		`{"work_id":"w","expected_revision":1,"note":"escape"}`,
	} {
		if _, err := owner.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if calls != 0 {
		t.Fatal("invalid creation reached store callback")
	}
	if _, err := owner.Call(context.Background(), Call{Arguments: []byte(`{"title":"p","steps":[{"title":"s"}]}`)}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	var schema struct {
		OneOf []struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(owner.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.OneOf) != 2 {
		t.Fatal("owner schema must have creation and edit alternatives")
	}
	for _, branch := range schema.OneOf {
		for _, forbidden := range []string{"work_id", "note", "blocker"} {
			if _, ok := branch.Properties[forbidden]; ok {
				t.Fatal("owner advertises", forbidden)
			}
		}
	}
	progressCalls := 0
	report := func(context.Context, Call, work.ProgressUpdate) (Result, error) {
		progressCalls++
		return Result{}, nil
	}
	implementor := UpdatePlan(nil, report)
	auditor := UpdateWork(report)
	for _, op := range []Tool{implementor, auditor} {
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(`{"title":"p","steps":[{"title":"s"}]}`)}); err == nil {
			t.Fatal("delegate created plan")
		}
	}
	if _, err := auditor.Call(context.Background(), Call{Arguments: []byte(`{"work_id":"a","expected_revision":1,"steps":[{"step_id":"s","status":"ready_for_review"}]}`)}); err == nil {
		t.Fatal("auditor changed implementation progress")
	}
	if _, err := auditor.Call(context.Background(), Call{Arguments: []byte(`{"work_id":"a","expected_revision":1,"blocker":"missing evidence"}`)}); !errors.Is(err, work.ErrInvalid) {
		t.Fatal(err)
	}
	if progressCalls != 0 {
		t.Fatal(progressCalls)
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
	op := SubmitAudit(func(context.Context, Call, work.AuditRequest) (Result, error) { return Result{}, nil })
	var schema struct {
		Branches []struct {
			Properties map[string]struct {
				Enum     []string `json:"enum"`
				MinItems *int     `json:"minItems"`
				MaxItems *int     `json:"maxItems"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(op.Definition().Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Branches) != 2 {
		t.Fatal("missing audit alternatives")
	}
	for _, branch := range schema.Branches {
		verdict := branch.Properties["verdict"].Enum[0]
		findings, exists := branch.Properties["findings"]
		if !exists {
			t.Fatalf("%s schema omits findings", verdict)
		}
		required := false
		for _, field := range branch.Required {
			if field == "findings" {
				required = true
			}
		}
		if verdict == "pass" && (required || findings.MaxItems == nil || *findings.MaxItems != 0) {
			t.Fatal("pass must allow optional empty findings only")
		}
		if verdict == "fail" && (!required || findings.MinItems == nil || *findings.MinItems != 1) {
			t.Fatal("fail must require nonempty findings")
		}
	}
}

func TestPlanRejectionNamesTheClosestForm(t *testing.T) {
	op := UpdatePlan(func(context.Context, Call, work.PlanUpdate) (Result, error) { return Result{}, nil }, nil)
	_, err := op.Call(context.Background(), Call{Arguments: []byte(`{"title":"p","steps":[{"title":"s","step_id":"step-1","status":"completed"}]}`)})
	if err == nil {
		t.Fatal("accepted step IDs on creation")
	}
	got := err.Error()
	for _, want := range []string{
		"update_plan arguments match no operation: ",
		"form 1, Create a plan with a title and new steps: arguments.steps[0].status is not an allowed field (also not allowed: step_id)",
		"form 2, Edit owned structure using plan_id and expected_revision: arguments.expected_revision is required (also required: plan_id)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rejection %q lacks %q", got, want)
		}
	}
}
