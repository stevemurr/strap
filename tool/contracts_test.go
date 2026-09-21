package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/work"
)

type EmbeddedFields struct {
	Value string `json:"value"`
}

func TestParameterConstructionRejectsInvalidConstraints(t *testing.T) {
	for _, rule := range []Constraint{{}, MinLength("title", -1), MinItems("title", 1), Minimum("count", -129), Enum("count", "a"), Enum("title"), Enum("title", string([]byte{255})), AtLeastOneNonNull("title", "value"), AtLeastOneNonNull(""), AtLeastOneNonNull("", "missing"), MinLength("title[]", 1)} {
		if _, err := NewParameters[contractArgs](contractConstraints(rule)...); err == nil {
			t.Fatalf("accepted constraint on %q", rule.path)
		}
	}
	if _, err := NewParameters[contractArgs](contractConstraints(Enum("title", "x"), MinLength("title", 2))...); err == nil {
		t.Fatal("inconsistent enum accepted")
	}
	checks := []func() error{
		func() error { _, e := NewParameters[struct{ hidden string }](); return e },
		func() error { _, e := NewParameters[struct{ Values []float64 }](); return e },
		func() error { _, e := NewParameters[struct{ *EmbeddedFields }](); return e },
		func() error {
			_, e := NewParameters[struct {
				EmbeddedFields `json:",omitempty"`
			}]()
			return e
		},
		func() error {
			_, e := NewParameters[struct {
				Value string `json:"bad\\name"`
			}]()
			return e
		},
		func() error {
			_, e := NewParameters[struct {
				Value string `json:"value"`
				EmbeddedFields
			}]()
			return e
		},
	}
	for i, check := range checks {
		if err := check(); err == nil {
			t.Fatalf("accepted invalid shape %d", i)
		}
	}
	var p Parameters[struct{}]
	if p.Schema() != nil {
		t.Fatal("uninitialized schema")
	}
}
func TestParameterValueTypesAndMalformedJSON(t *testing.T) {
	type args struct {
		Flag    bool     `json:"flag"`
		Number  int      `json:"number"`
		Values  []string `json:"values"`
		Ignored chan int `json:"-"`
	}
	p, err := NewParameters[args](MinItems("values", 1))
	if err != nil {
		t.Fatal(err)
	}
	good, err := p.Decode([]byte(`{"input":{"flag":true,"number":2.0,"values":["x"]}}`))
	if err != nil || !good.Flag || good.Number != 2 || len(good.Values) != 1 {
		t.Fatal(good, err)
	}
	for _, raw := range []string{`[]`, `{"input":{"flag":"true","number":1,"values":["x"]}}`, `{"input":{"flag":true,"number":"1","values":["x"]}}`, `{"input":{"flag":true,"number":1,"values":"x"}}`, `{"input":{"flag":true,"number":1,"values":[1]}}`, `{"input":{"flag":true,"number":1,"values":[]}}`, `{"flag":true,"number":1,"values":[`, `{"input":{"flag":true,1:2}}`, string([]byte{255})} {
		if _, err := p.Decode([]byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func TestInvalidFuncAndCompositionsFailBeforeDispatch(t *testing.T) {
	params, _ := NewParameters[struct{}]()
	handler := func(context.Context, Call, struct{}) (Result, error) { return Text("ok"), nil }
	for _, f := range []Func[struct{}]{{}, {Spec: Definition[struct{}]{Name: "x", Parameters: params}}, {Spec: Definition[struct{}]{Name: "x"}, Invoke: handler}} {
		if _, err := f.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{}}`)}); err == nil {
			t.Fatal("invalid tool accepted")
		}
	}
	good := Func[struct{}]{Spec: Definition[struct{}]{Name: "good", Parameters: params}, Invoke: handler}
	var nilTool *Func[struct{}]
	for _, branches := range [][]Tool{{nilTool}, {Func[struct{}]{}}, {good, good}} {
		if _, err := Compose(provider.ToolDefinition{Name: "composed"}, branches...); err == nil {
			t.Fatal("invalid composition accepted")
		}
	}
	if _, err := JSON(make(chan int)); err == nil {
		t.Fatal("serialized channel")
	}
}

type failingSender struct{}

func (failingSender) Send(context.Context, message.Draft) (message.Receipt, error) {
	return message.Receipt{}, errors.New("route unavailable")
}
func TestSenderAndLedgerToolCallbacks(t *testing.T) {
	for _, sender := range []message.Sender{nil, failingSender{}} {
		if _, err := SendMessage().Call(context.Background(), Call{Sender: sender, Arguments: json.RawMessage(`{"input":{"to":"agent","message":"hello"}}`)}); err == nil {
			t.Fatal("send failure lost")
		}
	}
	operations := []struct {
		tool Tool
		raw  string
	}{
		{GetWork(func(_ context.Context, _ Call, id work.ID) (Result, error) { return Text(string(id)), nil }), `{"input":{"work_id":"selected"}}`},
		{GetPlan(func(_ context.Context, _ Call, id work.PlanID) (Result, error) { return Text(string(id)), nil }), `{"input":{"plan_id":"selected"}}`},
		{GetAudit(func(_ context.Context, _ Call, id work.AuditID) (Result, error) { return Text(string(id)), nil }), `{"input":{"audit_id":"selected"}}`},
		{ReassignWork(func(_ context.Context, _ Call, r work.ReassignRequest) (Result, error) {
			if r.ExpectedRevision != 2 || r.Assignee != "new" {
				t.Error(r)
			}
			return Text(string(r.ID)), nil
		}), `{"input":{"work_id":"selected","expected_revision":2,"assignee":"new"}}`},
		{assignmentTool(t, "assign_audit", func(_ context.Context, _ Call, r work.AssignmentRequest) (Result, error) {
			if r.Kind != work.AuditWork || r.ExpectedRevision != 2 || r.SubmissionID != "sub" {
				t.Error(r)
			}
			return Text(string(r.WorkID)), nil
		}), `{"input":{"assignee":"auditor","work_id":"selected","expected_revision":2,"submission_id":"sub"}}`},
	}
	for _, op := range operations {
		result, err := op.tool.Call(context.Background(), Call{Arguments: json.RawMessage(op.raw)})
		if err != nil || result.Content.Text() != "selected" {
			t.Fatal(result, err)
		}
	}
	op := CreatePlan(func(_ context.Context, _ Call, r work.PlanUpdate) (Result, error) {
		if r.Steps[0].AcceptanceCriteria == nil || (*r.Steps[0].AcceptanceCriteria)[0] != "checked" {
			t.Error(r)
		}
		return Text("ok"), nil
	})
	if _, err := op.Call(context.Background(), Call{Arguments: json.RawMessage(`{"input":{"title":"plan","steps":[{"title":"step","acceptance_criteria":["checked"]}]}}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestAtLeastOneAndDefaultFieldNames(t *testing.T) {
	type args struct {
		Title   *string `json:"title"`
		ID      *string `json:"id"`
		Default int
	}
	p, err := NewParameters[args](Nullable("title", "unchanged"), Nullable("id", "new object"), AtLeastOneNonNull("", "title", "id"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"input":{"title":"new","Default":1,"id":null}}`, `{"input":{"id":"existing","Default":2,"title":null}}`} {
		got, err := p.Decode([]byte(raw))
		if err != nil || got.Default < 1 {
			t.Fatal(got, err)
		}
	}
	if _, err := p.Decode([]byte(`{"input":{"Default":0,"title":null,"id":null}}`)); err == nil {
		t.Fatal("empty patch accepted")
	}
}
func TestCompositionRequiresNameAndBranches(t *testing.T) {
	for _, def := range []provider.ToolDefinition{{}, {Name: "x", Parameters: json.RawMessage(`{}`)}, {Name: "x"}} {
		if _, err := Compose(def); err == nil {
			t.Fatal("invalid composition accepted")
		}
	}
}
