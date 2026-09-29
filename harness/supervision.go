package harness

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// supervisorReads give the debugger read access to the plans and work the
// session's manager owns, so it answers the user's questions about progress
// without messaging the manager. The ledger authorizes owners and assignees
// only, so each read runs as the manager. The debugger owns no work itself.
func (s *Session) supervisorReads() []tool.Tool {
	reads := map[string]bool{"get_audit": true, "get_plan": true, "get_work": true, "get_work_progress": true, "get_brief": true, "get_conclusion": true}
	var out []tool.Tool
	for _, t := range s.workflow.CoordinationTools() {
		if reads[t.Definition().Name] {
			out = append(out, asManager{s: s, read: t})
		}
	}
	return append(out, tool.ListWork(func(ctx context.Context, _ tool.Call, q work.ListQuery) (tool.Result, error) {
		v, err := s.ListWork(ctx, s.Manager(), q)
		if err != nil {
			return tool.Result{}, err
		}
		return modelJSON(v)
	}))
}

type asManager struct {
	s    *Session
	read tool.Tool
}

func (a asManager) Definition() provider.ToolDefinition { return a.read.Definition() }
func (a asManager) InputContract() tool.Contract {
	if typed, ok := a.read.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (a asManager) Validate() error { return tool.ValidateTool(a.read) }

func (a asManager) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	manager := a.s.Manager()
	if manager == "" {
		return tool.Result{}, errors.New("the session has no manager")
	}
	c.Actor = manager
	return a.read.Call(ctx, c)
}
