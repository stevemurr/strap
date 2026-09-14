package harness

import (
	"context"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

func (s *Session) ReportWorkProgress(ctx context.Context, actor identity.ActorID, r work.ReportWorkProgressRequest) (work.ReportWorkProgressResult, error) {
	return s.workflow.ReportWorkProgress(ctx, actor, r)
}
func (s *Session) GetWorkProgress(ctx context.Context, actor identity.ActorID, id work.ID) (work.WorkProgress, error) {
	v, err := s.workView(ctx)
	if err != nil {
		return work.WorkProgress{}, err
	}
	return v.GetWorkProgress(actor, id)
}
func (s *Session) GetWorkProgressReport(ctx context.Context, actor identity.ActorID, id work.ProgressReportID) (work.WorkProgressReport, error) {
	v, err := s.workView(ctx)
	if err != nil {
		return work.WorkProgressReport{}, err
	}
	return v.GetWorkProgressReport(actor, id)
}
func (s *Session) GetProgressFinding(ctx context.Context, actor identity.ActorID, id work.ProgressFindingID) (work.ProgressFinding, error) {
	v, err := s.workView(ctx)
	if err != nil {
		return work.ProgressFinding{}, err
	}
	return v.GetProgressFinding(actor, id)
}
