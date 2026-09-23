package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type plainShell struct{}

func (plainShell) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "shell", Description: "Run a command."}
}
func (plainShell) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Text(`{"started":true,"output":"ok\n","exit_code":0}`), nil
}

func TestWorkExecutionBindsSoleActiveAssignment(t *testing.T) {
	s := &Session{Store: work.New()}
	shell := s.withWorkExecution([]tool.Tool{plainShell{}})[0]
	call := func() tool.Result {
		t.Helper()
		r, err := shell.Call(context.Background(), tool.Call{Actor: "impl", InvocationID: "impl/tool-1"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(); r.Execution != nil || r.Content.Text() != `{"started":true,"output":"ok\n","exit_code":0}` {
		t.Fatal("unassigned run gained a receipt", r)
	}
	a, err := s.Store.AssignWork("root", work.AssignRequest{Assignee: "impl", Task: "A"})
	if err != nil {
		t.Fatal(err)
	}
	r := call()
	if r.Execution == nil || r.Execution.WorkID != string(a.ID) || r.Execution.AssignedAtRevision != uint64(a.AssignedAtRevision) || r.Execution.Actor != "impl" {
		t.Fatal("run not bound to its assignment", r.Execution)
	}
	var receipt struct {
		EvidenceRef string          `json:"evidence_ref"`
		Result      json.RawMessage `json:"result"`
	}
	if err = json.Unmarshal([]byte(r.Content.Text()), &receipt); err != nil || receipt.EvidenceRef != r.Execution.EvidenceRef || string(receipt.Result) != r.Captured.Text() {
		t.Fatal("receipt lost the shell result", r.Content.Text(), err)
	}
	if _, err = s.Store.AssignWork("root", work.AssignRequest{Assignee: "impl", Task: "B"}); err != nil {
		t.Fatal(err)
	}
	if r := call(); r.Execution != nil {
		t.Fatal("ambiguous run gained a receipt", r.Execution)
	}
}
