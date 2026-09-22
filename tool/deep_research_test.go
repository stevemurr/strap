package tool

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/research"
	"strings"
	"testing"
)

func TestDeepResearchRequiredNullableContract(t *testing.T) {
	calls := 0
	op := DeepResearch(func(_ context.Context, _ Call, r research.Request) (Result, error) {
		calls++
		if err := r.Validate(); err != nil {
			return Result{}, err
		}
		return Text("ok"), nil
	})
	raw, _ := MarshalInput(research.Request{WorkID: "work-a", Question: "Compare approaches", SuccessCriteria: []string{"Explain evidence"}})
	if _, err := op.Call(context.Background(), Call{Arguments: raw}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"context":null,`, "", 1), strings.Replace(string(raw), `["Explain evidence"]`, `[]`, 1), strings.Replace(string(raw), `"max_tokens":null`, `"max_tokens":0`, 1), strings.Replace(string(raw), `"question":"Compare approaches"`, `"question":null`, 1)} {
		if _, err := op.Call(context.Background(), Call{Arguments: json.RawMessage(bad)}); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if calls != 1 {
		t.Fatal("invalid input reached handler", calls)
	}
}
