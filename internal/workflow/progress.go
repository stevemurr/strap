package workflow

import (
	"context"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func (s *Session) ReportWorkProgress(ctx context.Context, actor identity.ActorID, r work.ReportWorkProgressRequest) (work.ReportWorkProgressResult, error) {
	return admitted(s, ctx, func(context.Context) (work.ReportWorkProgressResult, error) {
		w, err := s.Store.GetWork(actor, r.WorkID)
		if err != nil {
			return work.ReportWorkProgressResult{}, err
		}
		s.mu.Lock()
		reg, ok := s.roles[actor]
		s.mu.Unlock()
		if !ok || !reg.Role.Accepts(w.Kind) {
			return work.ReportWorkProgressResult{}, work.ErrForbidden
		}
		return s.Store.ReportWorkProgress(actor, r)
	})
}

func (s *Session) progressTool() tool.Tool {
	return tool.ReportWorkProgress(func(ctx context.Context, c tool.Call, r work.ReportWorkProgressRequest) (tool.Result, error) {
		v, err := s.ReportWorkProgress(ctx, c.Actor, r)
		return result(v, err)
	})
}
