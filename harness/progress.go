package harness

import (
	"context"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
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
func (s *Session) progressReadTool(brief bool) func(context.Context, tool.Call, tool.ProgressReadArgs) (tool.Result, error) {
	return func(ctx context.Context, c tool.Call, a tool.ProgressReadArgs) (tool.Result, error) {
		v, e := s.progressReads.ReadFamily(ctx, c.Actor, progressQuery(a), brief)
		if e != nil {
			return tool.Result{}, e
		}
		return tool.JSON(v)
	}
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

// wakeContext gives an agent its current plans, owned work and assignments
// at the start of each exchange, read from the same accepted-log view that
// admission uses. Agents with nothing owned or assigned receive nothing.
func (s *Session) wakeContext(actor identity.ActorID) agent.WakeContext {
	return func(ctx context.Context, _ []message.Message) (*message.Message, error) {
		head, err := s.progressReads.Reader.Head(ctx)
		if err != nil {
			return nil, err
		}
		v, err := s.progressReads.Reader.At(ctx, head.Cursor)
		if err != nil {
			return nil, err
		}
		state, err := v.ActorState(ctx, actor)
		if err != nil {
			return nil, err
		}
		if len(state.Plans)+len(state.Owned)+len(state.Assigned) == 0 {
			return nil, nil
		}
		return &message.Message{From: actor, To: actor, Kind: message.Observation, State: &state}, nil
	}
}

func (s *Session) inboxAdmission(actor identity.ActorID) agent.InboxAdmission {
	return func(ctx context.Context, inputs []message.Message) (agent.InboxDecision, error) {
		// Classification reads the notice alone, so admission never replays a
		// projection to decide whether to wake.
		head, err := s.progressReads.Reader.Head(ctx)
		if err != nil {
			return agent.InboxDecision{}, err
		}
		decision := agent.InboxDecision{Session: head.Cursor.Session, Through: head.Cursor.Sequence}
		for _, m := range inputs {
			decision.Wake = decision.Wake || workflow.ProgressNoticeWakes(m)
		}
		return decision, nil
	}
}

func (s *Session) lookupExecutionEvidence(ref string) (work.ExecutionEvidence, error) {
	v, err := s.progressReads.Reader.At(context.Background(), eventlog.Cursor{})
	if err != nil {
		return work.ExecutionEvidence{}, err
	}
	e, err := v.GetExecutionEvidence(context.Background(), s.Root(), ref)
	if err != nil {
		return work.ExecutionEvidence{}, err
	}
	b := e.Execution.Activity.Result.Execution
	return work.ExecutionEvidence{WorkID: work.ID(b.WorkID), AssignedAtRevision: work.Revision(b.AssignedAtRevision), Actor: b.Actor}, nil
}
