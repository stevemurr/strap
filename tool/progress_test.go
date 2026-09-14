package tool

import (
	"context"
	"github.com/stevemurr/strap/work"
	"testing"
)

func TestProgressReportRequiresExplicitBindingAndIntent(t *testing.T) {
	calls := 0
	op := ReportWorkProgress(func(context.Context, Call, work.ReportWorkProgressRequest) (Result, error) {
		calls++
		return Text("recorded"), nil
	})
	for _, raw := range []string{
		`{"work_id":"w","expected_revision":1,"position":{"objective":"inspect"}}`,
		`{"work_id":"w","expected_revision":1,"assigned_at_revision":1,"note":"legacy"}`,
		`{"work_id":"w","expected_revision":1,"assigned_at_revision":1}`,
		`{"work_id":"w","expected_revision":1,"assigned_at_revision":1,"position":null}`,
		`{"work_id":"w","expected_revision":1,"assigned_at_revision":1,"steps":[{"step_id":"s","status":"completed"}]}`,
	} {
		if _, err := op.Call(context.Background(), Call{Arguments: []byte(raw)}); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request reached handler")
	}
	if _, err := op.Call(context.Background(), Call{Arguments: []byte(`{"work_id":"w","expected_revision":1,"assigned_at_revision":1,"position":{"objective":"inspect","blocker":""}}`)}); err != nil {
		t.Fatal(err)
	}
}
