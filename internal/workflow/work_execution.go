package workflow

import (
	"context"
	"errors"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// workExecution gives a worker's shell runs host-issued evidence refs, so an
// observed finding about a command can cite the run itself. Without one,
// workers cited source files or invented refs for build and test results.
// The model-facing input is unchanged: a run binds to the actor's sole active
// assignment, and runs outside one return the plain shell result.
type workExecution struct {
	s     *Session
	shell tool.Tool
}

func (e workExecution) Definition() provider.ToolDefinition {
	d := e.shell.Definition()
	d.Description += " While you hold one active assignment, the result carries a host-issued evidence_ref for citing this run in progress findings."
	return d
}

// The wrapper keeps the shell's input contract; only the result changes.
func (e workExecution) InputContract() tool.Contract {
	if typed, ok := e.shell.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (e workExecution) Validate() error { return tool.ValidateTool(e.shell) }
func (e workExecution) BookkeepingParameters() []string {
	if b, ok := e.shell.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (e workExecution) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	w, ok, err := e.s.Store.AdmitExecution(c.Actor)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok {
		return e.shell.Call(ctx, c)
	}
	binding := &tool.ExecutionBinding{EvidenceRef: e.s.Store.NewExecutionRef(), WorkID: string(w.ID), AssignedAtRevision: uint64(w.AssignedAtRevision), Actor: c.Actor}
	result, err := e.shell.Call(ctx, c)
	// The shell already bounds its output, so the receipt adds no second bound.
	captured, encodeErr := tool.ExecutionResultWithin(result, binding, err, 0)
	return captured, errors.Join(err, encodeErr)
}

func (s *Session) withWorkExecution(tools []tool.Tool) []tool.Tool {
	for i, t := range tools {
		if t.Definition().Name == "shell" {
			tools[i] = workExecution{s: s, shell: t}
		}
	}
	return tools
}
