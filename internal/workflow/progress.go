package workflow

import (
	"context"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

func (s *Session) ReportWorkProgress(ctx context.Context, actor identity.ActorID, r work.ReportWorkProgressRequest) (work.ReportWorkProgressResult, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return work.ReportWorkProgressResult{}, err
	}
	defer done()
	if err = run.Err(); err != nil {
		return work.ReportWorkProgressResult{}, err
	}
	w, err := s.Store.GetWork(actor, r.ID)
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
}
