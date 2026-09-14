package workflow

import (
	"context"
	"encoding/json"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
	"sync/atomic"
	"testing"
	"time"
)

type diagnosticShell struct {
	calls  atomic.Int32
	during func()
}

func (*diagnosticShell) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{Name: "shell", Description: "Bounded diagnostic"}
}
func (d *diagnosticShell) Call(_ context.Context, c tool.Call) (tool.Result, error) {
	d.calls.Add(1)
	var m map[string]any
	json.Unmarshal(c.Arguments, &m)
	if len(m) != 1 || m["command"] != "inspect" {
		panic("leaked selectors")
	}
	d.during()
	return tool.Text("observed"), nil
}
func TestDiagnosticCapturesExplicitWorkBeforeExecution(t *testing.T) {
	shell := &diagnosticShell{}
	s := &Session{Store: work.New(), ctx: context.Background(), roles: map[identity.ActorID]roster.Registration{"r": {Role: roster.Researcher}}, researchShell: shell, researchMaxTimeout: time.Minute}
	a, err := s.Store.AssignResearch("root", work.ResearchAssignRequest{Assignee: "r", Task: "A"})
	if err != nil {
		t.Fatal(err)
	}
	s.Store.AssignResearch("root", work.ResearchAssignRequest{Assignee: "r", Task: "B"})
	shell.during = func() {
		if _, err := s.Store.Reassign("root", work.ReassignRequest{WorkTarget: work.WorkTarget{ID: a.ID, ExpectedRevision: a.Revision}, Assignee: "other"}); err != nil {
			t.Fatal(err)
		}
	}
	op := s.researchDiagnosticTool()
	args, _ := json.Marshal(tool.ResearchDiagnosticArgs{WorkID: a.ID, AssignedAtRevision: a.AssignedAtRevision, Command: "inspect"})
	result, err := op.Call(context.Background(), tool.Call{Actor: "r", Arguments: args, InvocationID: "r/tool-1"})
	if err != nil || result.Execution == nil || result.Execution.WorkID != string(a.ID) || result.Execution.Actor != "r" {
		t.Fatal(result, err)
	}
	for _, bad := range []json.RawMessage{args, json.RawMessage(`{"command":"inspect"}`)} {
		if _, err = op.Call(context.Background(), tool.Call{Actor: "r", Arguments: bad}); err == nil {
			t.Fatal("unauthorized command ran")
		}
	}
	if shell.calls.Load() != 1 {
		t.Fatal(shell.calls.Load())
	}
}
