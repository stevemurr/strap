package workflow

import (
	"context"
	"encoding/json"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/research"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type activeResearch struct {
	binding research.Binding
	cancel  context.CancelCauseFunc
}

func WithDeepResearch(e *research.Engine, web func(research.Binding) research.Retrieval, record research.Recorder, reader tool.Tool) Option {
	return func(s *Session) {
		s.deepResearch = e
		s.researchWeb = web
		s.researchRecord = record
		s.researchRead = reader
	}
}

// Called while researchMu is held by both registration and assignment mutations.
func (s *Session) retireResearchLocked(id work.ID, cause error) {
	if a := s.researchRun; a != nil && a.binding.WorkID == string(id) {
		a.cancel(cause)
	}
}
func (s *Session) deepResearchTool() tool.Tool {
	return tool.DeepResearch(func(ctx context.Context, c tool.Call, req research.Request) (tool.Result, error) {
		if err := s.deepResearch.Validate(req); err != nil {
			return tool.Result{}, err
		}
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
		s.researchMu.Lock()
		if s.researchRun != nil {
			s.researchMu.Unlock()
			return tool.Result{}, research.ErrBusy
		}
		w, err := s.Store.AdmitResearchDiagnostic(c.Actor, work.ID(req.WorkID))
		if err != nil {
			s.researchMu.Unlock()
			return tool.Result{}, err
		}
		ctx, cancel := context.WithCancelCause(run)
		b := research.Binding{WorkID: string(w.ID), Assignment: uint64(w.AssignedAtRevision), Actor: string(c.Actor), InvocationID: c.InvocationID}
		active := &activeResearch{binding: b, cancel: cancel}
		s.researchRun = active
		s.researchMu.Unlock()
		defer func() {
			cancel(nil)
			s.researchMu.Lock()
			if s.researchRun == active {
				s.researchRun = nil
			}
			s.researchMu.Unlock()
		}()
		check := func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return context.Cause(ctx)
			}
			current, err := s.Store.GetWork(identity.ActorID(b.Actor), work.ID(b.WorkID))
			if err != nil || current.State != work.Active || current.Assignee != identity.ActorID(b.Actor) || uint64(current.AssignedAtRevision) != b.Assignment {
				return research.ErrReassigned
			}
			return nil
		}
		report, err := s.deepResearch.Run(ctx, b, req, research.Dependencies{Web: s.researchWeb(b), Record: s.researchRecord, Check: check})
		if err != nil {
			return tool.Result{}, err
		}
		captured, err := json.Marshal(report)
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Captured: content.Text(string(captured)), Content: content.Text(string(research.Digest(report)))}, nil
	})
}
