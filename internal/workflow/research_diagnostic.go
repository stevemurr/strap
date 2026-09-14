package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
	"time"
)

func WithResearchDiagnostic(shell tool.Tool, maximum time.Duration) Option {
	return func(s *Session) { s.researchShell = shell; s.researchMaxTimeout = maximum }
}
func (s *Session) researchDiagnosticTool() tool.Tool {
	return tool.ResearchDiagnostic(s.researchShell.Definition().Description, s.researchMaxTimeout, func(ctx context.Context, c tool.Call, r tool.ResearchDiagnosticArgs) (tool.Result, error) {
		run, done, err := s.begin(ctx)
		if err != nil {
			return tool.Result{}, err
		}
		defer done()
		s.mu.Lock()
		reg, ok := s.roles[c.Actor]
		s.mu.Unlock()
		if !ok || reg.Role != roster.Researcher {
			return tool.Result{}, work.ErrForbidden
		}
		if err = run.Err(); err != nil {
			return tool.Result{}, err
		}
		w, err := s.Store.AdmitResearchDiagnostic(c.Actor, r.WorkID, r.AssignedAtRevision)
		if err != nil {
			return tool.Result{}, err
		}
		binding := &tool.ExecutionBinding{EvidenceRef: tool.NewExecutionEvidenceRef(), WorkID: string(w.ID), AssignedAtRevision: uint64(w.AssignedAtRevision), Actor: c.Actor}
		args, err := json.Marshal(struct {
			Command   string `json:"command"`
			TimeoutMS *int64 `json:"timeout_ms,omitempty"`
		}{r.Command, r.TimeoutMS})
		if err != nil {
			return tool.Result{}, err
		}
		c.Arguments = args
		result, err := s.researchShell.Call(run, c)
		captured, encodeErr := tool.ExecutionResult(result, binding, err)
		return captured, errors.Join(err, encodeErr)
	})
}
