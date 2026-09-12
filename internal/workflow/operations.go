package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
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
	w, err := s.GetWork(ctx, actor, id)
	if err != nil {
		return work.Inspection{}, err
	}
	v := work.Inspection{Work: w}
	if w.Scope != nil {
		p, e := s.Store.GetPlan(actor, w.Scope.PlanID)
		if e == nil {
			for _, step := range p.Steps {
				if slices.Contains(w.Scope.StepIDs, step.ID) {
					v.Steps = append(v.Steps, step)
				}
			}
		}
	}
	sid := w.LatestSubmissionID
	if w.Kind == work.AuditWork {
		sid = w.SubjectSubmissionID
	}
	if sid != "" {
		sub, e := s.Store.GetSubmission(actor, sid)
		if e != nil {
			return work.Inspection{}, e
		}
		v.Submission = &sub
	}
	if w.RequestedByAuditID != "" {
		a, e := s.Store.GetAudit(actor, w.RequestedByAuditID)
		if e != nil {
			return work.Inspection{}, e
		}
		v.Audit = &a
	}
	return v, nil
}

func (s *Session) eligible(id identity.ActorID, kind work.Kind) error {
	s.mu.Lock()
	role, ok := s.roles[id]
	s.mu.Unlock()
	if !ok || role != kind {
		return fmt.Errorf("agent %s is not provisioned for %s", id, kind)
	}
	info, err := s.Controller.InspectAgent(id, conversation.InspectOptions{})
	if err != nil {
		return err
	}
	if info.State.Terminal() {
		return fmt.Errorf("agent %s has exited", id)
	}
	return nil
}
func (s *Session) provision(parent, requested identity.ActorID, kind work.Kind) (identity.ActorID, bool, error) {
	if requested != "" {
		return requested, false, s.eligible(requested, kind)
	}
	spec := s.implementor
	if kind == work.AuditWork {
		spec = s.auditor
	}
	created, err := s.Controller.CreateAgent(parent, spec)
	if err != nil {
		return "", false, err
	}
	s.mu.Lock()
	s.roles[created.AgentID] = kind
	s.mu.Unlock()
	return created.AgentID, true, nil
}

// ReassignWork validates the current binding before provisioning a replacement.
// If the ledger rejects the mutation, a newly created agent is stopped.
func (s *Session) ReassignWork(ctx context.Context, actor identity.ActorID, r work.ReassignRequest) (work.Work, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Work{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Work{}, err
	}
	w, err := s.Store.GetWork(actor, r.ID)
	if err != nil {
		return work.Work{}, err
	}
	if w.Owner != actor {
		return work.Work{}, work.ErrForbidden
	}
	if w.State != work.Active {
		return work.Work{}, work.ErrState
	}
	if w.Revision != r.ExpectedRevision {
		return work.Work{}, work.ErrConflict
	}
	kind := work.Implementation
	if w.Kind == work.AuditWork {
		kind = work.AuditWork
	}
	id, created, err := s.provision(actor, r.Assignee, kind)
	if err != nil {
		return work.Work{}, err
	}
	r.Assignee = id
	if err = ctx.Err(); err == nil {
		w, err = s.Store.Reassign(actor, r)
	}
	if err != nil && created {
		_, stopErr := s.Controller.StopAgent(id)
		err = errors.Join(err, stopErr)
	}
	return w, err
}

// AssignWork provisions the configured role and registers its work for dispatch.
// It returns ledger state, not a delivery or completion acknowledgment.
func (s *Session) AssignWork(ctx context.Context, actor identity.ActorID, a work.AssignmentRequest) (work.Work, error) {
	run, done, admitErr := s.begin(ctx)
	if admitErr != nil {
		return work.Work{}, admitErr
	}
	defer done()
	ctx = run
	if err := ctx.Err(); err != nil {
		return work.Work{}, err
	}
	if actor == "" || actor != s.Root() {
		return work.Work{}, work.ErrForbidden
	}
	if a.Kind != work.Implementation && a.Kind != work.AuditWork {
		return work.Work{}, fmt.Errorf("%w: kind must be implementation or audit", work.ErrInvalid)
	}
	if a.Kind == work.Implementation {
		if strings.TrimSpace(a.Task) == "" || a.WorkID != "" || a.ExpectedRevision != 0 || a.SubmissionID != "" {
			return work.Work{}, fmt.Errorf("%w: implementation requires task and cannot select an audit submission", work.ErrInvalid)
		}
	} else {
		if a.WorkID == "" || a.ExpectedRevision == 0 || a.SubmissionID == "" || a.Scope != nil || a.Task != "" || a.Context != "" || a.ExpectedOutput != "" {
			return work.Work{}, fmt.Errorf("%w: audit requires work_id, expected_revision and submission_id; its task and scope are derived", work.ErrInvalid)
		}
	}
	id, created, provisionErr := s.provision(actor, a.Assignee, a.Kind)
	if provisionErr != nil {
		return work.Work{}, provisionErr
	}
	var w work.Work
	var err error
	if e := ctx.Err(); e != nil {
		err = e
	} else if a.Kind == work.Implementation {
		w, err = s.Store.AssignWork(actor, work.AssignRequest{Assignee: id, Task: a.Task, Context: a.Context, ExpectedOutput: a.ExpectedOutput, Scope: a.Scope})
	} else {
		w, err = s.Store.AssignAudit(actor, work.AssignAuditRequest{WorkTarget: work.WorkTarget{ID: a.WorkID, ExpectedRevision: a.ExpectedRevision}, SubmissionID: a.SubmissionID, Auditor: id})
	}
	if err != nil {
		if created {
			_, stopErr := s.Controller.StopAgent(id)
			err = errors.Join(err, stopErr)
		}
		return work.Work{}, err
	}
	return w, nil
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
