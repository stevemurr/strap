package harness

import (
	"context"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/workflow"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/tool"
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

func progressQuery(a tool.ProgressReadArgs) inspection.ProgressQuery {
	return inspection.ProgressQuery{Mode: a.Mode, WorkID: a.WorkID, ReportID: a.ReportID, FindingID: a.FindingID, BriefID: a.BriefID, EvidenceRef: a.EvidenceRef, Cursor: a.Cursor, Limit: a.Limit, MaxBytes: a.MaxBytes}
}
func (s *Session) readProgressTool(ctx context.Context, c tool.Call, a tool.ProgressReadArgs) (tool.Result, error) {
	v, e := s.progressReads.ReadFamily(ctx, c.Actor, progressQuery(a), false)
	if e != nil {
		return tool.Result{}, e
	}
	return tool.JSON(v)
}
func (s *Session) readBriefTool(ctx context.Context, c tool.Call, a tool.ProgressReadArgs) (tool.Result, error) {
	v, e := s.progressReads.ReadFamily(ctx, c.Actor, progressQuery(a), true)
	if e != nil {
		return tool.Result{}, e
	}
	return tool.JSON(v)
}
func (s *Session) ReadWorkProgress(ctx context.Context, actor identity.ActorID, q inspection.ProgressQuery) (inspection.ProgressPage, error) {
	return s.progressReads.ReadFamily(ctx, actor, q, false)
}
func (s *Session) ReadResearchBrief(ctx context.Context, actor identity.ActorID, q inspection.ProgressQuery) (inspection.ProgressPage, error) {
	return s.progressReads.ReadFamily(ctx, actor, q, true)
}
func (s *Session) ListWorkProgressReports(ctx context.Context, actor identity.ActorID, q work.ReportQuery) (work.ReportPage, error) {
	return s.progressReads.ListWorkProgressReports(ctx, actor, q)
}
func (s *Session) ListWorkProgressFindings(ctx context.Context, actor identity.ActorID, q work.ReportQuery) (work.ProgressFindingPage, error) {
	return s.progressReads.ListWorkProgressFindings(ctx, actor, q)
}

func (s *Session) inboxAdmission(actor identity.ActorID) agent.InboxAdmission {
	return func(ctx context.Context, inputs []message.Message) (agent.InboxDecision, error) {
		head, err := s.progressReads.Reader.Head(ctx)
		if err != nil {
			return agent.InboxDecision{}, err
		}
		v, err := s.progressReads.Reader.At(ctx, head.Cursor)
		if err != nil {
			return agent.InboxDecision{}, err
		}
		decision := agent.InboxDecision{Session: head.Cursor.Session, Through: head.Cursor.Sequence}
		get := func(a identity.ActorID, id work.ID) (work.Work, error) {
			w, e := v.InspectWork(ctx, a, id)
			return w.Work, e
		}
		for _, m := range inputs {
			wake, e := workflow.ProgressNoticeWakes(m, get)
			if e != nil {
				return decision, e
			}
			decision.Wake = decision.Wake || wake
		}
		return decision, nil
	}
}
