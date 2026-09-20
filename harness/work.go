package harness

import (
	"context"

	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/work"
)

// These are trusted host operations. Actor IDs select existing work authority;
// a remote adapter must authorize access to the session before calling them.
func (s *Session) UpdatePlan(ctx context.Context, actor identity.ActorID, u work.PlanUpdate) (work.Plan, error) {
	return s.workflow.UpdatePlan(ctx, actor, u)
}

func (s *Session) CancelWork(ctx context.Context, actor identity.ActorID, r work.CancelRequest) (work.Work, error) {
	return s.workflow.CancelWork(ctx, actor, r)
}
func (s *Session) SubmitWork(ctx context.Context, actor identity.ActorID, r work.SubmitRequest) (work.SubmitReceipt, error) {
	return s.workflow.SubmitWork(ctx, actor, r)
}
func (s *Session) SubmitAudit(ctx context.Context, actor identity.ActorID, r work.AuditRequest) (work.Audit, error) {
	return s.workflow.SubmitAudit(ctx, actor, r)
}
func (s *Session) GetPlan(ctx context.Context, actor identity.ActorID, id work.PlanID) (work.Plan, error) {
	view, err := s.workView(ctx)
	if err != nil {
		return work.Plan{}, err
	}
	return view.GetPlan(actor, id)
}
func (s *Session) GetWork(ctx context.Context, actor identity.ActorID, id work.ID) (work.Work, error) {
	view, err := s.workView(ctx)
	if err != nil {
		return work.Work{}, err
	}
	return view.GetWork(actor, id)
}
func (s *Session) GetSubmission(ctx context.Context, actor identity.ActorID, id work.SubmissionID) (work.Submission, error) {
	view, err := s.workView(ctx)
	if err != nil {
		return work.Submission{}, err
	}
	return view.GetSubmission(actor, id)
}
func (s *Session) GetAudit(ctx context.Context, actor identity.ActorID, id work.AuditID) (work.Audit, error) {
	view, err := s.workView(ctx)
	if err != nil {
		return work.Audit{}, err
	}
	return view.GetAudit(actor, id)
}
func (s *Session) InspectWork(ctx context.Context, actor identity.ActorID, id work.ID) (work.Inspection, error) {
	view, err := s.workView(ctx)
	if err != nil {
		return work.Inspection{}, err
	}
	return view.InspectWork(actor, id)
}
func (s *Session) ReassignWork(ctx context.Context, actor identity.ActorID, r work.ReassignRequest) (work.Work, error) {
	return s.workflow.ReassignWork(ctx, actor, r)
}
func (s *Session) AssignWork(ctx context.Context, actor identity.ActorID, a work.AssignmentRequest) (work.Work, error) {
	return s.workflow.AssignWork(ctx, actor, a)
}

func (s *Session) SubmitResearch(ctx context.Context, actor identity.ActorID, r work.SubmitResearchRequest) (work.SubmitResearchResult, error) {
	return s.workflow.SubmitResearch(ctx, actor, r)
}
func (s *Session) GetResearchBrief(ctx context.Context, actor identity.ActorID, id work.ResearchBriefID) (work.ResearchBrief, error) {
	v, err := s.workView(ctx)
	if err != nil {
		return work.ResearchBrief{}, err
	}
	return v.GetResearchBrief(actor, id)
}
