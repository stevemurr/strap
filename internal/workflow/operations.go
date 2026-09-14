package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
)

// UpdatePlan edits the root's plan through the shared application boundary. The actor
// is supplied by the host or bound tool runtime, never inferred from request data.
// Cancellation is checked before ledger entry; it is not a rollback guarantee.
func (s *Session) UpdatePlan(ctx context.Context, actor identity.ActorID, u work.PlanUpdate) (work.Plan, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Plan{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Plan{}, err
	}
	if actor == "" || actor != s.Root() {
		return work.Plan{}, work.ErrForbidden
	}
	return s.Store.UpdatePlan(actor, u)
}

func (s *Session) UpdateProgress(ctx context.Context, actor identity.ActorID, u work.ProgressUpdate) (work.Work, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Work{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Work{}, err
	}
	return s.Store.UpdateProgress(actor, u)
}

func (s *Session) CancelWork(ctx context.Context, actor identity.ActorID, r work.CancelRequest) (work.Work, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Work{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Work{}, err
	}
	return s.Store.Cancel(actor, r)
}

func (s *Session) SubmitWork(ctx context.Context, actor identity.ActorID, r work.SubmitRequest) (work.Submission, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Submission{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Submission{}, err
	}
	return s.Store.SubmitWork(actor, r)
}

func (s *Session) SubmitAudit(ctx context.Context, actor identity.ActorID, r work.AuditRequest) (work.Audit, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Audit{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Audit{}, err
	}
	return s.Store.SubmitAudit(actor, r)
}

func (s *Session) GetPlan(ctx context.Context, actor identity.ActorID, id work.PlanID) (work.Plan, error) {
	if err := ctx.Err(); err != nil {
		return work.Plan{}, err
	}
	return s.Store.GetPlan(actor, id)
}

func (s *Session) GetWork(ctx context.Context, actor identity.ActorID, id work.ID) (work.Work, error) {
	if err := ctx.Err(); err != nil {
		return work.Work{}, err
	}
	return s.Store.GetWork(actor, id)
}

func (s *Session) GetSubmission(ctx context.Context, actor identity.ActorID, id work.SubmissionID) (work.Submission, error) {
	if err := ctx.Err(); err != nil {
		return work.Submission{}, err
	}
	return s.Store.GetSubmission(actor, id)
}

func (s *Session) GetAudit(ctx context.Context, actor identity.ActorID, id work.AuditID) (work.Audit, error) {
	if err := ctx.Err(); err != nil {
		return work.Audit{}, err
	}
	return s.Store.GetAudit(actor, id)
}

// InspectWork collects scoped work and immutable submission/audit details. The
// returned fields are independent snapshots, not an atomic execution checkpoint.
func (s *Session) InspectWork(ctx context.Context, actor identity.ActorID, id work.ID) (work.Inspection, error) {
	if err := ctx.Err(); err != nil {
		return work.Inspection{}, err
	}
	return s.Store.InspectWork(actor, id)
}

// RegisterRoot records bootstrap identity without exposing root creation to tools.
func (s *Session) RegisterRoot() error {
	id := s.Root()
	if id == "" {
		return work.ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.register(roster.Registration{AgentID: id, Parent: "user", Role: roster.Root})
}

// register runs under mu. Required publication precedes eligibility; readers
// attempting assignment while publication completes wait for this commit.
func (s *Session) register(r roster.Registration) error {
	if _, ok := s.roles[r.AgentID]; ok {
		return fmt.Errorf("%w: agent already registered", work.ErrState)
	}
	if err := s.emit(conversation.AgentRegistered{Registration: r}); err != nil {
		return err
	}
	s.roles[r.AgentID] = r
	return nil
}
func (s *Session) CreateAgent(ctx context.Context, actor identity.ActorID, r roster.CreateRequest) (roster.Registration, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return roster.Registration{}, err
	}
	defer done()
	if actor == "" || actor != s.Root() {
		return roster.Registration{}, work.ErrForbidden
	}
	if !r.Role.Creatable() {
		return roster.Registration{}, fmt.Errorf("%w: role must be implementor, auditor, or researcher", work.ErrInvalid)
	}
	spec := s.implementor
	if r.Role == roster.Auditor {
		spec = s.auditor
	}
	if r.Role == roster.Researcher {
		spec = s.researcher
	}
	if spec.Provider == nil {
		return roster.Registration{}, fmt.Errorf("%w: role is not configured", work.ErrInvalid)
	}
	if err = run.Err(); err != nil {
		return roster.Registration{}, err
	}
	created, err := s.Controller.CreateAgent(actor, spec)
	if err != nil {
		return roster.Registration{}, err
	}
	registration := roster.Registration{AgentID: created.AgentID, Parent: actor, Role: r.Role}
	s.mu.Lock()
	err = run.Err()
	if err == nil {
		err = s.register(registration)
	}
	s.mu.Unlock()
	if err != nil {
		_, stopErr := s.Controller.StopAgent(created.AgentID)
		return roster.Registration{}, errors.Join(err, stopErr)
	}
	return registration, nil
}
func (s *Session) eligible(id identity.ActorID, kind work.Kind) error {
	if id == "" {
		return fmt.Errorf("%w: assignee is required", work.ErrInvalid)
	}
	s.mu.Lock()
	registration, ok := s.roles[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: agent %s is not registered", work.ErrInvalid, id)
	}
	if !registration.Role.Accepts(kind) {
		return fmt.Errorf("%w: agent %s has role %s, incompatible with %s work", work.ErrInvalid, id, registration.Role, kind)
	}
	info, err := s.Controller.InspectAgent(id, conversation.InspectOptions{})
	if err != nil {
		return err
	}
	if info.State.Terminal() || info.State == agent.StopRequested {
		return fmt.Errorf("%w: agent %s is %s", work.ErrState, id, info.State)
	}
	return nil
}

// ReassignWork transfers active work to an explicitly selected existing agent.
func (s *Session) ReassignWork(ctx context.Context, actor identity.ActorID, r work.ReassignRequest) (work.Work, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return work.Work{}, err
	}
	defer done()
	if actor == "" || actor != s.Root() {
		return work.Work{}, work.ErrForbidden
	}
	w, err := s.Store.GetWork(actor, r.ID)
	if err != nil {
		return work.Work{}, err
	}
	if w.State != work.Active {
		return work.Work{}, work.ErrState
	}
	if w.Revision != r.ExpectedRevision {
		return work.Work{}, work.ErrConflict
	}
	if err = s.eligible(r.Assignee, w.Kind); err != nil {
		return work.Work{}, err
	}
	if err = run.Err(); err != nil {
		return work.Work{}, err
	}
	return s.Store.Reassign(actor, r)
}

// AssignWork registers work for asynchronous dispatch without creating an agent.
func (s *Session) AssignWork(ctx context.Context, actor identity.ActorID, r work.AssignmentRequest) (work.Work, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return work.Work{}, err
	}
	defer done()
	if actor == "" || actor != s.Root() {
		return work.Work{}, work.ErrForbidden
	}
	if err = r.Validate(); err != nil {
		return work.Work{}, err
	}
	if err = s.eligible(r.Assignee, r.Kind); err != nil {
		return work.Work{}, err
	}
	if err = run.Err(); err != nil {
		return work.Work{}, err
	}
	switch r.Kind {
	case work.Implementation:
		return s.Store.AssignWork(actor, work.AssignRequest{Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope})
	case work.Research:
		return s.Store.AssignResearch(actor, work.ResearchAssignRequest{Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput})
	case work.AuditWork:
		return s.Store.AssignAudit(actor, work.AssignAuditRequest{WorkTarget: work.WorkTarget{ID: r.WorkID, ExpectedRevision: r.ExpectedRevision}, SubmissionID: r.SubmissionID, Auditor: r.Assignee})
	case work.Repair:
		return s.Store.AssignRepair(actor, work.AssignRepairRequest{WorkTarget: work.WorkTarget{ID: r.WorkID, ExpectedRevision: r.ExpectedRevision}, AuditID: r.AuditID, Assignee: r.Assignee})
	}
	return work.Work{}, work.ErrInvalid
}

func (s *Session) begin(ctx context.Context) (context.Context, func(), error) {
	if s.admission != nil {
		return s.admission.Begin(ctx)
	}
	if s.closing.Load() {
		return nil, nil, conversation.ErrClosed
	}
	return ctx, func() {}, ctx.Err()
}

func (s *Session) SubmitResearch(ctx context.Context, actor identity.ActorID, r work.SubmitResearchRequest) (work.SubmitResearchResult, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return work.SubmitResearchResult{}, err
	}
	defer done()
	if err = run.Err(); err != nil {
		return work.SubmitResearchResult{}, err
	}
	return s.Store.SubmitResearch(actor, r)
}
