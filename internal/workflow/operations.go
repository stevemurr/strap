package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/stevemurr/strap/message"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/internal/admission"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// UpdatePlan edits the manager's plan through the shared application boundary. The actor
// is supplied by the host or bound tool runtime, never inferred from request data.
// Cancellation is checked before ledger entry; it is not a rollback guarantee.
func (s *Session) UpdatePlan(ctx context.Context, actor identity.ActorID, u work.PlanUpdate) (work.Plan, error) {
	return admitted(s, ctx, func(context.Context) (work.Plan, error) {
		if !s.Coordinator(actor) {
			return work.Plan{}, work.ErrForbidden
		}
		return s.Store.UpdatePlan(actor, u)
	})
}

func (s *Session) CancelWork(ctx context.Context, actor identity.ActorID, r work.CancelRequest) (work.Work, error) {
	return admitted(s, ctx, func(context.Context) (work.Work, error) {
		s.researchMu.Lock()
		defer s.researchMu.Unlock()
		w, err := s.Store.Cancel(actor, r)
		if err == nil {
			s.retireResearchLocked(r.ID, context.Canceled)
		}
		return w, err
	})
}

func (s *Session) SubmitWork(ctx context.Context, actor identity.ActorID, r work.SubmitRequest) (work.SubmitReceipt, error) {
	return admitted(s, ctx, func(context.Context) (work.SubmitReceipt, error) { return s.Store.SubmitWork(actor, r) })
}

func (s *Session) SubmitAudit(ctx context.Context, actor identity.ActorID, r work.AuditRequest) (work.Audit, error) {
	return admitted(s, ctx, func(context.Context) (work.Audit, error) { return s.Store.SubmitAudit(actor, r) })
}

// admitted runs op inside the admission window once the admitted context is
// still live. Operations that must validate before checking liveness use begin.
func admitted[T any](s *Session, ctx context.Context, op func(context.Context) (T, error)) (T, error) {
	var zero T
	run, done, err := s.begin(ctx)
	if err != nil {
		return zero, err
	}
	defer done()
	if err = run.Err(); err != nil {
		return zero, err
	}
	return op(run)
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

// register runs under mu. The graph admits the registration before it is
// published and adds it, with the edges its role derives, after; readers
// attempting assignment while publication completes wait for this commit.
func (s *Session) register(r roster.Registration) error {
	if err := s.graph.Admit(r); err != nil {
		return fmt.Errorf("%w: %v", work.ErrState, err)
	}
	if err := s.emit(conversation.AgentRegistered{Registration: r}); err != nil {
		return err
	}
	return s.graph.Add(r)
}

// Graph is the session's agent topology, shared with the host that enforces it.
func (s *Session) Graph() *roster.Graph { return s.graph }

// Coordinator reports whether actor may plan, create workers and assign work:
// only the session's manager, which the user talks to directly.
func (s *Session) Coordinator(actor identity.ActorID) bool {
	if actor == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.graph.Role(actor) == roster.Manager
}

// UseManager configures the session's manager. The manager plans, assigns,
// reads and reports; it holds no work itself, so it gets no tools to change
// the workspace or to submit.
func (s *Session) UseManager(spec agent.Spec) {
	spec = spec.Clone()
	s.mu.Lock()
	s.manager = spec
	s.mu.Unlock()
}

// ManagerSpec returns the configured manager for trusted assembly inspection.
func (s *Session) ManagerSpec() agent.Spec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manager.Clone()
}

// CreateManager creates the session's one manager, the agent the user talks
// to. The bootstrap calls it once; it is not a tool, and a second call fails.
func (s *Session) CreateManager(ctx context.Context) (identity.ActorID, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	s.mu.Lock()
	existing := s.graph.Find(roster.Manager)
	s.mu.Unlock()
	if existing != "" {
		return "", fmt.Errorf("%w: the session already has its manager %s", work.ErrForbidden, existing)
	}
	spec := s.ManagerSpec()
	if spec.Provider == nil {
		return "", fmt.Errorf("%w: manager is not configured", work.ErrInvalid)
	}
	r, err := s.create(run, message.User, roster.Manager, spec)
	return r.AgentID, err
}

// CreateDebugger creates the session's debugger, which the user talks to
// beside the manager. The bootstrap calls it once, after the manager, and
// only in a debug session; it is not a tool.
func (s *Session) CreateDebugger(ctx context.Context, spec agent.Spec) (identity.ActorID, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	if spec.Provider == nil {
		return "", fmt.Errorf("%w: debugger is not configured", work.ErrInvalid)
	}
	r, err := s.create(run, message.User, roster.Debugger, spec)
	return r.AgentID, err
}

// CreateSolo creates a single-agent session's one agent, which the user talks
// to and which does all the work itself. The bootstrap calls it in place of
// CreateManager; it is not a tool.
func (s *Session) CreateSolo(ctx context.Context, spec agent.Spec) (identity.ActorID, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	if spec.Provider == nil {
		return "", fmt.Errorf("%w: the agent is not configured", work.ErrInvalid)
	}
	r, err := s.create(run, message.User, roster.Agent, spec)
	return r.AgentID, err
}

// CreateAgent starts an idle worker for the manager. Only the manager creates
// agents, and only workers.
func (s *Session) CreateAgent(ctx context.Context, actor identity.ActorID, r roster.CreateRequest) (roster.Registration, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return roster.Registration{}, err
	}
	defer done()
	if !s.Coordinator(actor) {
		return roster.Registration{}, work.ErrForbidden
	}
	spec, err := s.roleSpec(r.Role)
	if err != nil {
		return roster.Registration{}, err
	}
	return s.create(run, actor, r.Role, spec)
}

// roleSpec is the spec a manager's new agent of role runs.
func (s *Session) roleSpec(role roster.Role) (agent.Spec, error) {
	var spec agent.Spec
	switch role {
	case roster.Implementor:
		spec = s.implementor
	case roster.Auditor:
		spec = s.auditor
	case roster.WebResearcher:
		spec = s.webResearcher
	case roster.DeepResearcher:
		spec = s.deepResearcher
		if spec.Provider == nil {
			return agent.Spec{}, fmt.Errorf("%w: deep research is not enabled in this session; use a web_researcher (kind web_research)", work.ErrInvalid)
		}
	case roster.Reviewer:
		spec = s.reviewer
		if spec.Provider == nil {
			return agent.Spec{}, fmt.Errorf("%w: this session has no workspace to review", work.ErrInvalid)
		}
	case roster.Experimenter:
		spec = s.experimenter
		if spec.Provider == nil {
			return agent.Spec{}, fmt.Errorf("%w: experiments need local tools, which this session does not have", work.ErrInvalid)
		}
	default:
		return agent.Spec{}, fmt.Errorf("%w: role must be implementor, auditor, reviewer, web_researcher, deep_researcher, or experimenter", work.ErrInvalid)
	}
	if spec.Provider == nil {
		return agent.Spec{}, fmt.Errorf("%w: role is not configured", work.ErrInvalid)
	}
	return spec, nil
}

// create starts and registers an agent under parent, stopping it again if
// registration fails.
func (s *Session) create(run context.Context, parent identity.ActorID, role roster.Role, spec agent.Spec) (roster.Registration, error) {
	if err := run.Err(); err != nil {
		return roster.Registration{}, err
	}
	created, err := s.Controller.CreateAgent(parent, spec)
	if err != nil {
		return roster.Registration{}, err
	}
	registration := roster.Registration{AgentID: created.AgentID, Parent: parent, Role: role}
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

// eligible reports whether id may take kind work from actor. A manager takes
// work only from itself.
func (s *Session) eligible(actor, id identity.ActorID, kind work.Kind) error {
	if id == "" {
		return fmt.Errorf("%w: assignee is required", work.ErrInvalid)
	}
	s.mu.Lock()
	registration, ok := s.graph.Registration(id)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: agent %s is not registered", work.ErrInvalid, id)
	}
	if !registration.Role.Accepts(kind) {
		return fmt.Errorf("%w: agent %s has role %s, incompatible with %s work, which goes to a %s", work.ErrInvalid, id, registration.Role, kind, roster.RoleFor(kind))
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

// AssignWork registers work for asynchronous dispatch without creating an agent.
func (s *Session) AssignWork(ctx context.Context, actor identity.ActorID, r work.AssignmentRequest) (work.Work, error) {
	run, done, err := s.begin(ctx)
	if err != nil {
		return work.Work{}, err
	}
	defer done()
	if !s.Coordinator(actor) {
		return work.Work{}, work.ErrForbidden
	}
	if err = r.Validate(); err != nil {
		return work.Work{}, err
	}
	// A role named as the assignee staffs the work with a new agent of that
	// role: creating and assigning otherwise took the manager two model turns.
	var created identity.ActorID
	role := roster.Role(r.Assignee)
	if role.Worker() && !staffable(role, r.Kind) && r.Kind != work.AuditWork {
		return work.Work{}, fmt.Errorf("%w: %s work goes to a %s, not a %s", work.ErrInvalid, r.Kind, roster.RoleFor(r.Kind), role)
	}
	if staffable(role, r.Kind) {
		spec, err := s.roleSpec(role)
		if err != nil {
			return work.Work{}, err
		}
		registration, err := s.create(run, actor, role, spec)
		if err != nil {
			return work.Work{}, err
		}
		created, r.Assignee = registration.AgentID, registration.AgentID
	}
	w, err := s.assign(run, actor, r)
	if err != nil && created != "" {
		_, stopErr := s.Controller.StopAgent(created)
		err = errors.Join(err, stopErr)
	}
	return w, err
}

// staffable reports whether naming role as the assignee of kind work creates
// its agent. Audits have their own auditors, created by the workflow.
func staffable(role roster.Role, kind work.Kind) bool {
	return role != roster.Auditor && role.Worker() && role.Accepts(kind)
}

func (s *Session) assign(run context.Context, actor identity.ActorID, r work.AssignmentRequest) (work.Work, error) {
	if err := s.eligible(actor, r.Assignee, r.Kind); err != nil {
		return work.Work{}, err
	}
	if err := run.Err(); err != nil {
		return work.Work{}, err
	}
	switch r.Kind {
	case work.Implementation:
		return s.Store.AssignWork(actor, work.AssignRequest{Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope})
	case work.Review, work.WebResearch, work.DeepResearch:
		return s.Store.AssignInvestigation(actor, work.InvestigationRequest{Kind: r.Kind, Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope})
	case work.Experiment:
		return s.Store.AssignExperiment(actor, work.ExperimentAssignRequest{Assignee: r.Assignee, Task: r.Task, Context: r.Context, ExpectedOutput: r.ExpectedOutput, Scope: r.Scope})
	case work.AuditWork:
		var brief string
		if original, err := s.Store.GetWork(actor, r.WorkID); err == nil {
			brief = s.brief(original, r.SubmissionID)
		}
		return s.Store.AssignAudit(actor, work.AssignAuditRequest{WorkTarget: work.WorkTarget{ID: r.WorkID, ExpectedRevision: r.ExpectedRevision}, SubmissionID: r.SubmissionID, Auditor: r.Assignee, Brief: brief})
	case work.Repair:
		return s.Store.AssignRepair(actor, work.AssignRepairRequest{WorkTarget: work.WorkTarget{ID: r.WorkID, ExpectedRevision: r.ExpectedRevision}, AuditID: r.AuditID, Assignee: r.Assignee})
	}
	return work.Work{}, work.ErrInvalid
}

func (s *Session) begin(ctx context.Context) (context.Context, func(), error) {
	if s.admission != nil {
		run, done, err := s.admission.Begin(ctx)
		if errors.Is(err, admission.ErrSuspended) {
			err = conversation.ErrInterrupted
		}
		if err == nil && s.executionHeld() {
			done()
			return nil, nil, conversation.ErrInterrupted
		}
		return run, done, err
	}
	if s.closing.Load() {
		return nil, nil, conversation.ErrClosed
	}
	if s.executionHeld() {
		return nil, nil, conversation.ErrInterrupted
	}
	return ctx, func() {}, ctx.Err()
}

func (s *Session) executionHeld() bool {
	return s.interrupted.Load() || (s.Controller != nil && s.Controller.Interrupted())
}

func (s *Session) brief(original work.Work, submission work.SubmissionID) string {
	if s.auditBrief == nil {
		return ""
	}
	return s.auditBrief(original, submission)
}

// assignAudit assigns a submitted implementation's audit to a new auditor on
// behalf of its owner. Every audit gets an auditor that never held work, as
// assign_audit requires of the owner.
func (s *Session) assignAudit(w work.Work) error {
	current, err := s.Store.GetWork(w.Owner, w.ID)
	if err != nil {
		return err
	}
	if current.Kind != work.Implementation || current.State != work.NeedsCheck || current.LatestSubmissionID == "" {
		return nil // Nothing to audit, or it is already being audited.
	}
	run, done, err := s.begin(s.ctx)
	if err != nil {
		return err
	}
	defer done()
	reg, err := s.create(run, w.Owner, roster.Auditor, s.auditor)
	if err != nil {
		return err
	}
	if _, err = s.Store.AssignAudit(w.Owner, work.AssignAuditRequest{WorkTarget: work.WorkTarget{ID: current.ID, ExpectedRevision: current.Revision}, SubmissionID: current.LatestSubmissionID, Auditor: reg.AgentID, Brief: s.brief(current, current.LatestSubmissionID)}); err != nil {
		_, stopErr := s.Controller.StopAgent(reg.AgentID)
		return errors.Join(err, stopErr)
	}
	return nil
}

// SubmitExperiment captures the method's files from the experimenter's copy
// of the workspace and delivers the conclusion.
func (s *Session) SubmitExperiment(ctx context.Context, actor identity.ActorID, a tool.SubmitExperimentInput) (work.SubmitExperimentResult, error) {
	var files []work.MethodFile
	if len(a.Method.Files) > 0 {
		if s.methodFiles == nil {
			return work.SubmitExperimentResult{}, fmt.Errorf("%w: this session keeps no workspace copies to capture method files from; set method.files to null", work.ErrInvalid)
		}
		var err error
		if files, err = s.methodFiles(actor, a.Method.Files); err != nil {
			return work.SubmitExperimentResult{}, err
		}
	}
	return admitted(s, ctx, func(context.Context) (work.SubmitExperimentResult, error) {
		return s.Store.SubmitExperiment(actor, a.Request(files))
	})
}

func (s *Session) SubmitBrief(ctx context.Context, actor identity.ActorID, r work.SubmitBriefRequest) (work.SubmitBriefResult, error) {
	// Findings delivered with the brief are recorded first, as the report a
	// researcher otherwise spent a separate model turn on, with the same checks.
	if drafts := r.Findings; len(drafts) > 0 {
		r.Findings = nil
		w, err := s.Store.GetWork(actor, r.ID)
		if err != nil || w.Revision != r.ExpectedRevision {
			// Let the store report the stale or unknown target.
			return admitted(s, ctx, func(context.Context) (work.SubmitBriefResult, error) { return s.Store.SubmitBrief(actor, r) })
		}
		report, err := s.ReportWorkProgress(ctx, actor, work.ReportWorkProgressRequest{WorkID: r.ID, Findings: drafts})
		if err != nil {
			return work.SubmitBriefResult{}, fmt.Errorf("findings not recorded, nothing delivered: %w", err)
		}
		r.FindingIDs = append(r.FindingIDs, report.FindingIDs...)
		r.ExpectedRevision = report.WorkRevision
		result, err := admitted(s, ctx, func(context.Context) (work.SubmitBriefResult, error) { return s.Store.SubmitBrief(actor, r) })
		if err != nil {
			return result, fmt.Errorf("%w; the findings were recorded as %v: cite them in finding_ids instead of repeating findings", err, report.FindingIDs)
		}
		return result, nil
	}
	return admitted(s, ctx, func(context.Context) (work.SubmitBriefResult, error) { return s.Store.SubmitBrief(actor, r) })
}
