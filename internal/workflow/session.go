// Package workflow wires the work ledger to agents without putting scheduling
// policy in the conversation controller or the work store.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type binding struct {
	work             work.ID
	revision         work.Revision
	recipient, owner identity.ActorID
}

// Session is the application's single consumer of controller events. Host reads
// consume a separate relay, so work dispatch proceeds even with no UI reader.
type Session struct {
	*conversation.Controller
	Store                *work.Store
	implementor, auditor agent.Spec
	mu                   sync.Mutex
	roles                map[identity.ActorID]work.Kind
	ctx                  context.Context
	cancel               context.CancelFunc
	events               *inbox.Inbox[conversation.Event]
	done                 chan struct{}
}

func New(ctx context.Context, c *conversation.Controller, implementor, auditor agent.Spec) *Session {
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{Controller: c, Store: work.New(), implementor: implementor.Clone(), auditor: auditor.Clone(), roles: map[identity.ActorID]work.Kind{}, ctx: ctx, cancel: cancel, events: inbox.New[conversation.Event](), done: make(chan struct{})}
	s.implementor.Tools = append(s.implementor.Tools, s.commonTools()...)
	s.implementor.Tools = append(s.implementor.Tools, tool.UpdatePlan(nil, s.updateProgress))
	s.implementor.Tools = append(s.implementor.Tools, tool.SubmitWork(func(_ context.Context, c tool.Call, r work.SubmitRequest) (tool.Result, error) {
		v, e := s.Store.SubmitWork(c.Actor, r)
		return result(v, e)
	}))
	s.auditor.Tools = append(s.auditor.Tools, s.commonTools()...)
	s.auditor.Tools = append(s.auditor.Tools, tool.UpdateWork(s.updateProgress))
	s.auditor.Tools = append(s.auditor.Tools, tool.SubmitAudit(func(_ context.Context, c tool.Call, r work.AuditRequest) (tool.Result, error) {
		v, e := s.Store.SubmitAudit(c.Actor, r)
		return result(v, e)
	}))
	go s.run()
	return s
}
func result(v any, err error) (tool.Result, error) {
	if err != nil {
		return tool.Result{}, err
	}
	return tool.JSON(v)
}
func (s *Session) commonTools() []tool.Tool {
	return []tool.Tool{
		tool.GetAudit(func(_ context.Context, c tool.Call, id work.AuditID) (tool.Result, error) {
			v, e := s.Store.GetAudit(c.Actor, id)
			return result(v, e)
		}),
		tool.GetPlan(func(_ context.Context, c tool.Call, id work.PlanID) (tool.Result, error) {
			v, e := s.Store.GetPlan(c.Actor, id)
			return result(v, e)
		}),
		tool.GetWork(func(_ context.Context, c tool.Call, id work.ID) (tool.Result, error) { return s.getWork(c.Actor, id) }),
	}
}
func (s *Session) getWork(actor identity.ActorID, id work.ID) (tool.Result, error) {
	w, err := s.Store.GetWork(actor, id)
	if err != nil {
		return tool.Result{}, err
	}
	v := struct {
		Work       work.Work        `json:"work"`
		Steps      []work.Step      `json:"steps,omitempty"`
		Submission *work.Submission `json:"submission,omitempty"`
		Audit      *work.Audit      `json:"audit,omitempty"`
	}{Work: w}
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
			return tool.Result{}, e
		}
		v.Submission = &sub
	}
	if w.RequestedByAuditID != "" {
		a, e := s.Store.GetAudit(actor, w.RequestedByAuditID)
		if e != nil {
			return tool.Result{}, e
		}
		v.Audit = &a
	}
	return tool.JSON(v)
}
func (s *Session) updateProgress(_ context.Context, c tool.Call, u work.ProgressUpdate) (tool.Result, error) {
	v, e := s.Store.UpdateProgress(c.Actor, u)
	return result(v, e)
}
func (s *Session) RootTools() []tool.Tool {
	return append(s.commonTools(),
		tool.UpdatePlan(func(_ context.Context, c tool.Call, u work.PlanUpdate) (tool.Result, error) {
			v, e := s.Store.UpdatePlan(c.Actor, u)
			return result(v, e)
		}, nil),
		tool.AssignWork(s.assign),
		tool.CancelWork(func(_ context.Context, c tool.Call, r work.CancelRequest) (tool.Result, error) {
			v, e := s.Store.Cancel(c.Actor, r)
			return result(v, e)
		}),
		tool.ReassignWork(s.reassign),
	)
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
func (s *Session) reassign(ctx context.Context, c tool.Call, r work.ReassignRequest) (tool.Result, error) {
	w, err := s.Store.GetWork(c.Actor, r.ID)
	if err != nil {
		return tool.Result{}, err
	}
	if w.Owner != c.Actor {
		return tool.Result{}, work.ErrForbidden
	}
	if w.State != work.Active {
		return tool.Result{}, work.ErrState
	}
	if w.Revision != r.ExpectedRevision {
		return tool.Result{}, work.ErrConflict
	}
	kind := work.Implementation
	if w.Kind == work.AuditWork {
		kind = work.AuditWork
	}
	id, created, err := s.provision(c.Actor, r.Assignee, kind)
	if err != nil {
		return tool.Result{}, err
	}
	r.Assignee = id
	if err = ctx.Err(); err == nil {
		w, err = s.Store.Reassign(c.Actor, r)
	}
	if err != nil && created {
		_, stopErr := s.Controller.StopAgent(id)
		err = errors.Join(err, stopErr)
	}
	return result(w, err)
}
func (s *Session) assign(ctx context.Context, c tool.Call, a tool.AssignWorkArgs) (tool.Result, error) {
	if c.Actor != s.Root() {
		return tool.Result{}, work.ErrForbidden
	}
	if a.Kind != work.Implementation && a.Kind != work.AuditWork {
		return tool.Result{}, fmt.Errorf("kind must be implementation or audit")
	}
	if a.Kind == work.Implementation {
		if strings.TrimSpace(a.Task) == "" || a.WorkID != "" || a.ExpectedRevision != 0 || a.SubmissionID != "" {
			return tool.Result{}, fmt.Errorf("implementation requires task and cannot select an audit submission")
		}
	} else {
		if a.WorkID == "" || a.ExpectedRevision == 0 || a.SubmissionID == "" || a.Scope != nil || a.Task != "" || a.Context != "" || a.ExpectedOutput != "" {
			return tool.Result{}, fmt.Errorf("audit requires work_id, expected_revision and submission_id; its task and scope are derived")
		}
	}
	id, created, provisionErr := s.provision(c.Actor, a.Assignee, a.Kind)
	if provisionErr != nil {
		return tool.Result{}, provisionErr
	}
	var w work.Work
	var err error
	if e := ctx.Err(); e != nil {
		err = e
	} else if a.Kind == work.Implementation {
		w, err = s.Store.AssignWork(c.Actor, work.AssignRequest{Assignee: id, Task: a.Task, Context: a.Context, ExpectedOutput: a.ExpectedOutput, Scope: a.Scope})
	} else {
		w, err = s.Store.AssignAudit(c.Actor, work.AssignAuditRequest{WorkTarget: work.WorkTarget{ID: a.WorkID, ExpectedRevision: a.ExpectedRevision}, SubmissionID: a.SubmissionID, Auditor: id})
	}
	if err != nil {
		if created {
			_, stopErr := s.Controller.StopAgent(id)
			err = errors.Join(err, stopErr)
		}
		return tool.Result{}, err
	}
	return tool.JSON(w)
}
func (s *Session) NextEvent(ctx context.Context) (conversation.Event, error) {
	return s.events.Receive(ctx)
}
func (s *Session) Close(ctx context.Context) error {
	err := s.Controller.Close(ctx)
	s.cancel()
	select {
	case <-s.done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}
func (s *Session) current(b binding) bool {
	w, e := s.Store.GetWork(b.owner, b.work)
	return e == nil && w.Assignee == b.recipient && w.AssignedAtRevision == b.revision && w.State == work.Active
}
func (s *Session) failure(b binding, detail string) {
	if !s.current(b) {
		return
	}
	_, err := s.Controller.Deliver(b.owner, message.Draft{To: b.owner, Kind: message.Notification, Content: fmt.Sprintf("Work %s delivery/execution needs attention for assignee %s: %s. Inspect work and reassign or cancel it; this is not an audit verdict.", b.work, b.recipient, detail)})
	if err != nil {
		_ = s.events.Send(conversation.MessageEvent{Message: message.Message{From: b.owner, To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work %s requires recovery: %s (owner notification failed: %v)", b.work, detail, err)}})
	}
}
func (s *Session) run() {
	defer close(s.done)
	defer s.events.Close()
	incoming := make(chan conversation.Event)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(incoming)
		for {
			e, err := s.Controller.NextEvent(s.ctx)
			if err != nil {
				return
			}
			select {
			case incoming <- e:
			case <-s.ctx.Done():
				return
			}
		}
	}()
	defer func() { s.cancel(); <-readerDone }()
	delivered := map[message.MessageID]binding{}
	failed := map[binding]bool{}
	attempted := map[work.EventID]bool{}
	published := map[work.EventID]bool{}
	revoked := map[work.EventID]bool{}
	notifications := map[message.MessageID]work.EventID{}
	drain := func() {
		for _, e := range s.Store.PendingEvents(0) {
			if !published[e.ID] {
				_ = s.events.Send(conversation.WorkEvent{Event: e.Clone()})
				published[e.ID] = true
			}
			w := e.Work
			if !revoked[e.ID] && (e.Kind == work.WorkCancelled || e.Kind == work.WorkReassigned) {
				revoked[e.ID] = true
				recipients := map[identity.ActorID]bool{}
				if e.Kind == work.WorkCancelled {
					recipients[w.Assignee] = true
				} else {
					for _, old := range delivered {
						if old.work == w.ID && old.revision != w.AssignedAtRevision && old.recipient != w.Assignee {
							recipients[old.recipient] = true
						}
					}
				}
				for recipient := range recipients {
					info, err := s.Controller.InspectAgent(recipient, conversation.InspectOptions{})
					if err == nil && !info.State.Terminal() {
						if _, err = s.Controller.Deliver(e.Actor, message.Draft{To: recipient, Kind: message.Notification, Event: &e}); err != nil {
							_ = s.events.Send(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work %s changed but recipient %s could not be notified: %v", w.ID, recipient, err)}})
						}
					}
				}
			}
			assignment := e.Kind == work.WorkAssigned || e.Kind == work.WorkReassigned
			if assignment {
				b := binding{w.ID, w.AssignedAtRevision, w.Assignee, w.Owner}
				if !s.current(b) {
					_ = s.Store.AcknowledgeEvent(e.ID)
					delete(attempted, e.ID)
					delete(published, e.ID)
					delete(revoked, e.ID)
					continue
				}
				if attempted[e.ID] {
					continue
				}
				// Delivery is attempted once per binding. Definite failures remain pending
				// for owner recovery; a new assignment creates a new event and binding.
				attempted[e.ID] = true
				receipt, err := s.Controller.Deliver(w.RequestedBy, message.Draft{To: w.Assignee, Kind: message.Instruction, Work: &w})
				if err != nil {
					s.failure(b, err.Error())
					failed[b] = true
					continue
				}
				delivered[receipt.MessageID] = b
			} else if e.Kind != work.PlanChanged && (w.Owner != e.Actor || e.Kind == work.ReviewRequested) {
				kind := message.Observation
				if e.Actionable {
					kind = message.Notification
				}
				if attempted[e.ID] {
					continue
				}
				attempted[e.ID] = true
				receipt, err := s.Controller.Deliver(e.Actor, message.Draft{To: w.Owner, Kind: kind, Event: &e})
				if err != nil {
					_ = s.events.Send(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work event %s for %s remains pending: %v", e.Kind, w.ID, err)}})
					continue
				}
				notifications[receipt.MessageID] = e.ID
				continue // Keep owner events pending until actually consumed.
			}
			_ = s.Store.AcknowledgeEvent(e.ID)
			delete(attempted, e.ID)
			delete(published, e.ID)
			delete(revoked, e.ID)
		}
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.Store.Ready():
			drain()
		case e, ok := <-incoming:
			if !ok {
				return
			}
			_ = s.events.Send(e)
			switch event := e.(type) {
			case conversation.AckEvent:
				if id, ok := notifications[event.Receipt.MessageID]; ok {
					if event.Receipt.Status == message.Consumed {
						_ = s.Store.AcknowledgeEvent(id)
						delete(notifications, event.Receipt.MessageID)
						delete(attempted, id)
						delete(published, id)
						delete(revoked, id)
					} else if event.Receipt.Status == message.Undelivered {
						delete(notifications, event.Receipt.MessageID)
						_ = s.events.Send(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work event %s remains pending: owner did not consume notification %s", id, event.Receipt.MessageID)}})
					}
				}
				if event.Receipt.Status == message.Undelivered {
					if b, ok := delivered[event.Receipt.MessageID]; ok && !failed[b] {
						s.failure(b, event.Receipt.Detail)
						failed[b] = true
					}
				}
			case conversation.AgentExited:
				for _, b := range delivered {
					if b.recipient == event.Agent && !failed[b] {
						s.failure(b, "agent exited before its work was resolved")
						failed[b] = true
					}
				}
			}
		}
	}
}
