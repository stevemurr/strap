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
	"path/filepath"
	"sync/atomic"
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
		return modelJSON(v)
	}
}
func (s *Session) ReadWorkProgress(ctx context.Context, actor identity.ActorID, q inspection.ProgressQuery) (inspection.ProgressPage, error) {
	return s.progressReads.ReadFamily(ctx, actor, q, false)
}
func (s *Session) ReadBrief(ctx context.Context, actor identity.ActorID, q inspection.ProgressQuery) (inspection.ProgressPage, error) {
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
// admission uses, and, if it can read files, on its first exchange a listing
// of the working directory. Agents with nothing to add receive nothing.
func (s *Session) wakeContext(actor identity.ActorID, spec agent.Spec) agent.WakeContext {
	var listed atomic.Bool
	// Only an agent that can read files is shown them; one without workspace
	// tools, shown the listing, planned the work itself instead of passing it
	// on (probe of ladder easy-03: 20 of 60 samples passed it on with the
	// listing, 42 without).
	listed.Store(!readsFiles(spec))
	return func(ctx context.Context, inputs []message.Message) (*message.Message, error) {
		// A solo agent's finish check judges only what this exchange changed.
		if s.finish != nil && actor == s.Manager() {
			s.finish.reset(inputs)
		}
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
		var workspace *message.Workspace
		if s.config.LocalTools && !listed.Swap(true) {
			if dir, err := filepath.Abs(s.config.Dir); err == nil {
				if real, err := filepath.EvalSymlinks(dir); err == nil {
					dir = real
				}
				workspace = s.listWorkspace(actor, dir)
			}
		}
		empty := len(state.Plans)+len(state.Owned)+len(state.Assigned) == 0
		var note string
		if s.finish != nil && actor == s.Manager() && s.finish.testerRequested() {
			note = testerNote
		}
		if empty && workspace == nil && note == "" {
			return nil, nil
		}
		m := &message.Message{From: actor, To: actor, Kind: message.Observation, Workspace: workspace, Content: note}
		if !empty {
			m.State = &state
		}
		return m, nil
	}
}

func readsFiles(spec agent.Spec) bool {
	for _, t := range spec.Tools {
		switch t.Definition().Name {
		case "read_file", "list_directory", "glob":
			return true
		}
	}
	return false
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
			// A reply to an assignment the ledger already resolved is consumed
			// into history without waking: the ledger notice is the one trigger.
			decision.Wake = decision.Wake || workflow.ProgressNoticeWakes(m) && !s.workflow.ResolvedAssignmentReply(m)
		}
		return decision, nil
	}
}

func (s *Session) lookupExecutionEvidence(ref string) (work.ExecutionEvidence, error) {
	v, err := s.progressReads.Reader.At(context.Background(), eventlog.Cursor{})
	if err != nil {
		return work.ExecutionEvidence{}, err
	}
	// All work belongs to the session's manager, so the host reads as it.
	e, err := v.GetExecutionEvidence(context.Background(), s.Manager(), ref)
	if err != nil {
		return work.ExecutionEvidence{}, err
	}
	b := e.Execution.Activity.Result.Execution
	return work.ExecutionEvidence{WorkID: work.ID(b.WorkID), AssignedAtRevision: work.Revision(b.AssignedAtRevision), Actor: b.Actor}, nil
}
