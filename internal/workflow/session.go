// Package workflow wires the work ledger to agents without putting scheduling
// policy in the conversation controller or the work store.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/internal/admission"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

type binding struct {
	work             work.ID
	revision         work.Revision
	recipient, owner identity.ActorID
}

// Session follows delivery/exit facts to dispatch ledger work. Harness hosts read
// those facts from the accepted log; standalone hosts retain a legacy event relay.
type Session struct {
	interrupted        atomic.Bool
	interruptDrain     chan chan error
	evidenceLookup     work.EvidenceLookup
	researchShell      tool.Tool
	researchMaxTimeout time.Duration
	publish            func(conversation.Event) error
	progressReads      []tool.Tool
	progressConfig     WorkProgressReportingConfig
	progressCurrent    func(identity.ActorID, work.ID) (work.Work, error)
	closing            atomic.Bool
	admission          *admission.Gate
	stopOwner          func() bool
	*conversation.Controller
	Store                *work.Store
	implementor, auditor agent.Spec
	researcher           agent.Spec
	mu                   sync.Mutex
	roles                map[identity.ActorID]roster.Registration
	ctx                  context.Context
	cancel               context.CancelFunc
	events               *inbox.Inbox[conversation.Event]
	done                 chan struct{}
}

type Option func(*Session)

func WithEvidenceLookup(f work.EvidenceLookup) Option {
	return func(s *Session) { s.evidenceLookup = f }
}
func WithProgressCurrent(f func(identity.ActorID, work.ID) (work.Work, error)) Option {
	return func(s *Session) { s.progressCurrent = f }
}

func WithProgressTools(ops []tool.Tool) Option {
	return func(s *Session) { s.progressReads = append([]tool.Tool(nil), ops...) }
}

func WithResearcher(spec agent.Spec) Option   { return func(s *Session) { s.researcher = spec.Clone() } }
func (s *Session) ResearcherSpec() agent.Spec { return s.researcher.Clone() }

// WithPublisher replaces the legacy host relay. It acknowledges required work records independently of the dispatcher.
func WithPublisher(p func(conversation.Event) error) Option {
	return func(s *Session) { s.publish = p }
}

func WithAdmission(g *admission.Gate) Option { return func(s *Session) { s.admission = g } }

func New(ctx context.Context, c *conversation.Controller, implementor, auditor agent.Spec, options ...Option) *Session {
	owner := ctx
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Session{Controller: c, Store: work.New(), implementor: implementor.Clone(), auditor: auditor.Clone(), roles: map[identity.ActorID]roster.Registration{}, progressConfig: DefaultWorkProgressReporting(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.interruptDrain = make(chan chan error)
	for _, option := range options {
		option(s)
	}
	if s.publish != nil {
		s.Store = work.New(work.WithEvidenceLookup(s.evidenceLookup), work.WithReporter(work.ReporterFunc(func(_ context.Context, e work.Event) error { return s.publish(conversation.WorkEvent{Event: e}) })))
	}
	if s.publish == nil {
		s.events = inbox.New[conversation.Event]()
	}
	s.implementor.Tools = append(s.implementor.Tools, s.commonTools()...)
	s.implementor.Tools = append(s.implementor.Tools, s.progressTool())
	s.implementor.Tools = append(s.implementor.Tools, tool.SubmitWork(func(ctx context.Context, c tool.Call, r work.SubmitRequest) (tool.Result, error) {
		v, e := s.SubmitWork(ctx, c.Actor, r)
		return result(v, e)
	}))
	s.auditor.Tools = append(s.auditor.Tools, s.commonTools()...)
	s.auditor.Tools = append(s.auditor.Tools, s.progressTool())
	s.auditor.Tools = append(s.auditor.Tools, tool.SubmitAudit(func(ctx context.Context, c tool.Call, r work.AuditRequest) (tool.Result, error) {
		v, e := s.SubmitAudit(ctx, c.Actor, r)
		return result(v, e)
	}))
	if s.researcher.Provider != nil {
		if s.researchShell != nil {
			s.researcher.Tools = append(s.researcher.Tools, s.researchDiagnosticTool())
		}
		s.researcher.Tools = append(s.researcher.Tools, s.commonTools()...)
		s.researcher.Tools = append(s.researcher.Tools, s.progressTool(), tool.WaitForInput())
		s.researcher.Tools = append(s.researcher.Tools, tool.SubmitResearch(func(ctx context.Context, c tool.Call, r work.SubmitResearchRequest) (tool.Result, error) {
			v, e := s.SubmitResearch(ctx, c.Actor, r)
			return result(v, e)
		}))
	}
	s.stopOwner = context.AfterFunc(owner, func() { _ = s.Close(context.Background()) })
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
	return append([]tool.Tool{
		tool.GetAudit(func(ctx context.Context, c tool.Call, id work.AuditID) (tool.Result, error) {
			v, e := s.GetAudit(ctx, c.Actor, id)
			return result(v, e)
		}),
		tool.GetPlan(func(ctx context.Context, c tool.Call, id work.PlanID) (tool.Result, error) {
			v, e := s.GetPlan(ctx, c.Actor, id)
			return result(v, e)
		}),
		tool.GetWork(func(ctx context.Context, c tool.Call, id work.ID) (tool.Result, error) {
			v, err := s.InspectWork(ctx, c.Actor, id)
			return result(v, err)
		}),
	}, s.progressReads...)
}
func (s *Session) updateProgress(ctx context.Context, c tool.Call, u work.ProgressUpdate) (tool.Result, error) {
	v, e := s.UpdateProgress(ctx, c.Actor, u)
	return result(v, e)
}
func (s *Session) RootTools() []tool.Tool {
	tools := append(s.commonTools(),
		tool.WaitForInput(),
		tool.CreateAgent(func(ctx context.Context, c tool.Call, r roster.CreateRequest) (tool.Result, error) {
			v, e := s.CreateAgent(ctx, c.Actor, r)
			return result(v, e)
		}))
	tools = append(tools, tool.PlanTools(func(ctx context.Context, c tool.Call, u work.PlanUpdate) (tool.Result, error) {
		v, e := s.UpdatePlan(ctx, c.Actor, u)
		return result(v, e)
	})...)
	tools = append(tools, tool.AssignmentTools(func(ctx context.Context, c tool.Call, r work.AssignmentRequest) (tool.Result, error) {
		v, err := s.AssignWork(ctx, c.Actor, r)
		return result(v, err)
	})...)
	return append(tools,
		tool.CancelWork(func(ctx context.Context, c tool.Call, r work.CancelRequest) (tool.Result, error) {
			v, e := s.CancelWork(ctx, c.Actor, r)
			return result(v, e)
		}),
		tool.ReassignWork(func(ctx context.Context, c tool.Call, r work.ReassignRequest) (tool.Result, error) {
			v, err := s.ReassignWork(ctx, c.Actor, r)
			return result(v, err)
		}),
	)
}
func (s *Session) emit(e conversation.Event) error {
	if s.publish != nil {
		return s.publish(e)
	} else {
		return s.events.Send(e)
	}
}

func (s *Session) NextEvent(ctx context.Context) (conversation.Event, error) {
	return s.events.Receive(ctx)
}

// BeginClosing stops new application mutations and dispatch while readers drain.
func (s *Session) BeginClosing() {
	s.closing.Store(true)
	if s.admission != nil {
		s.admission.Seal()
	}
}
func (s *Session) Close(ctx context.Context) error {
	s.BeginClosing()
	if err := s.Controller.Close(ctx); err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Session) current(b binding) bool {
	w, e := s.Store.GetWork(b.owner, b.work)
	return e == nil && w.Assignee == b.recipient && w.AssignedAtRevision == b.revision && w.State == work.Active
}
func (s *Session) failure(b binding, detail string) {
	if s.interrupted.Load() || !s.current(b) {
		return
	}
	_, err := s.Controller.Deliver(b.owner, message.Draft{To: b.owner, Kind: message.Notification, Content: fmt.Sprintf("Work %s delivery/execution needs attention for assignee %s: %s. Inspect work and reassign or cancel it; this is not an audit verdict.", b.work, b.recipient, detail)})
	if err != nil {
		s.emit(conversation.MessageEvent{Message: message.Message{From: b.owner, To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work %s requires recovery: %s (owner notification failed: %v)", b.work, detail, err)}})
	}
}
func (s *Session) run() {
	defer func() {
		if s.stopOwner != nil {
			s.stopOwner()
		}
	}()
	defer close(s.done)
	defer func() {
		if s.events != nil {
			s.events.Close()
		}
	}()
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
	notifications := map[message.MessageID][]work.EventID{}
	queue := newNoticeQueue(s.progressConfig)
	if s.progressCurrent == nil {
		s.progressCurrent = s.Store.GetWork
	}
	priorWorks := map[work.ID]work.Work{}
	coverages := map[work.EventID][]message.ProgressCoverage{}
	seenChange := map[work.EventID]bool{}
	retire := func() {
		if err := queue.retire(s.progressCurrent, func(id work.EventID) { _ = s.Store.AcknowledgeEvent(id) }); err != nil {
			s.emit(conversation.DiagnosticEvent{Level: "error", Message: err.Error()})
		}
	}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var timerC <-chan time.Time
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
		if due := queue.next(); !due.IsZero() {
			timer.Reset(max(time.Until(due), time.Nanosecond))
			timerC = timer.C
		}
	}

	outstanding := map[work.EventID]int{}
	sendParts := func(owner identity.ActorID, n message.WorkProgressNotice, ids []work.EventID, event *work.Event) bool {
		parts, err := splitNotice(n, s.progressConfig.MaxReportsPerNotice)
		if err != nil {
			s.emit(conversation.DiagnosticEvent{Level: "error", Message: err.Error()})
			return false
		}
		for i, part := range parts {
			var outcome *work.Event
			if i == len(parts)-1 {
				outcome = event
			}
			receipt, err := s.Controller.Deliver(owner, message.Draft{To: owner, Kind: message.Notification, Progress: &part, Event: outcome})
			if err != nil {
				if !errors.Is(err, conversation.ErrInterrupted) {
					s.emit(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Progress notice delivery failed: %v", err)}})
				}
				return false
			}
			notifications[receipt.MessageID] = append([]work.EventID(nil), ids...)
			for _, id := range ids {
				outstanding[id]++
			}
		}
		return true
	}
	sendNotice := func(owner identity.ActorID, n message.WorkProgressNotice, ids []work.EventID) bool {
		return sendParts(owner, n, ids, nil)
	}

	drain := func() {
		for _, e := range s.Store.PendingEvents(0) {
			if !published[e.ID] {
				if s.publish == nil {
					s.emit(conversation.WorkEvent{Event: e.Clone()})
				}
				published[e.ID] = true
			}
			if s.closing.Load() || s.interrupted.Load() {
				continue
			}
			w := e.Work
			if !seenChange[e.ID] {
				coverages[e.ID] = coverageChange(priorWorks, e.Change)
				seenChange[e.ID] = true
				retire()
			}
			if e.Kind == work.WorkProgressReported || e.Kind == work.ResearchDelivered {
				if attempted[e.ID] {
					continue
				}
				attempted[e.ID] = true
				if e.Kind == work.ResearchDelivered {
					sendNotice(w.Owner, message.WorkProgressNotice{Covered: coverages[e.ID], Briefs: []message.ResearchBriefRef{{WorkID: w.ID, AssignedAtRevision: w.AssignedAtRevision, WorkRevision: w.Revision, BriefID: w.LatestResearchBriefID}}, Attention: true}, []work.EventID{e.ID})
				} else if e.Actionable {
					sendNotice(w.Owner, message.WorkProgressNotice{Reports: []message.ProgressReportRef{{WorkID: w.ID, AssignedAtRevision: w.AssignedAtRevision, WorkRevision: w.Revision, ReportID: w.LatestProgressReportID}}, Attention: true}, []work.EventID{e.ID})
				} else if e.Change != nil && len(e.Change.ProgressReports) > 0 && len(e.Change.ProgressReports[0].Findings) > 0 {
					queue.add(e, time.Now())
				} else {
					_ = s.Store.AcknowledgeEvent(e.ID)
				}
				continue
			}
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
						if _, err = s.Controller.Deliver(e.Actor, message.Draft{To: recipient, Kind: message.Notification, Event: &e}); err != nil && !errors.Is(err, conversation.ErrInterrupted) {
							s.emit(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work %s changed but recipient %s could not be notified: %v", w.ID, recipient, err)}})
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
				if refs := coverages[e.ID]; len(refs) > 0 {
					sendParts(w.Owner, message.WorkProgressNotice{Covered: refs, Attention: true}, []work.EventID{e.ID}, &e)
					continue
				}
				receipt, err := s.Controller.Deliver(e.Actor, message.Draft{To: w.Owner, Kind: kind, Event: &e})
				if err != nil {
					if !errors.Is(err, conversation.ErrInterrupted) {
						s.emit(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work event %s for %s remains pending: %v", e.Kind, w.ID, err)}})
					}
					continue
				}
				notifications[receipt.MessageID] = []work.EventID{e.ID}
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
		case result := <-s.interruptDrain:
			err := s.cancelInterruptedWork()
			clear(delivered)
			clear(failed)
			clear(attempted)
			clear(published)
			clear(revoked)
			clear(notifications)
			clear(outstanding)
			clear(coverages)
			clear(seenChange)
			clear(priorWorks)
			queue = newNoticeQueue(s.progressConfig)
			resetTimer()
			result <- err
		case <-s.ctx.Done():
			return
		case <-s.Store.Ready():
			drain()
			resetTimer()
		case now := <-timerC:
			if !s.closing.Load() && !s.interrupted.Load() {
				drain()
				retire()
				queue.flush(now, sendNotice)
				resetTimer()
			}
		case e, ok := <-incoming:
			if !ok {
				s.closing.Store(true)
				drain()
				return
			}
			if s.publish == nil {
				s.emit(e)
			}
			if s.interrupted.Load() {
				continue // Settlement retires these notices without waking agents.
			}
			switch event := e.(type) {
			case conversation.AckEvent:
				if ids, ok := notifications[event.Receipt.MessageID]; ok {
					for _, id := range ids {
						if event.Receipt.Status == message.Consumed {
							if outstanding[id] > 1 {
								outstanding[id]--
								continue
							}
							delete(outstanding, id)
							_ = s.Store.AcknowledgeEvent(id)
							delete(notifications, event.Receipt.MessageID)
							delete(attempted, id)
							delete(published, id)
							delete(revoked, id)
						} else if event.Receipt.Status == message.Undelivered {
							delete(notifications, event.Receipt.MessageID)
							s.emit(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Work event %s remains pending: owner did not consume notification %s", id, event.Receipt.MessageID)}})
						}
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

// Specs returns immutable role configuration for trusted assembly inspection.
func (s *Session) Specs() (agent.Spec, agent.Spec) { return s.implementor.Clone(), s.auditor.Clone() }
