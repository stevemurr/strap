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
	"github.com/stevemurr/strap/research"
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
	researchMu      sync.Mutex
	researchRun     *activeResearch
	deepResearch    *research.Engine
	researchWeb     func(research.Binding) research.Retrieval
	researchRecord  research.Recorder
	researchRead    tool.Tool
	interrupted     atomic.Bool
	interruptDrain  chan chan error
	evidenceLookup  work.EvidenceLookup
	publish         func(conversation.Event) error
	progressReads   []tool.Tool
	progressConfig  WorkProgressReportingConfig
	progressCurrent func(identity.ActorID, work.ID) (work.Work, error)
	closing         atomic.Bool
	admission       *admission.Gate
	stopOwner       func() bool
	*conversation.Controller
	Store                *work.Store
	implementor, auditor agent.Spec
	webResearcher        agent.Spec
	deepResearcher       agent.Spec
	experimenter         agent.Spec
	autoAudit            bool
	auditBrief           AuditBrief
	reviewer             agent.Spec
	methodFiles          MethodFiles
	manager              agent.Spec
	mu                   sync.Mutex
	graph                *roster.Graph                 // The agent topology; see roster.Graph.
	assignments          map[message.MessageID]binding // Delivered assignment messages; guarded by mu.
	ctx                  context.Context
	cancel               context.CancelFunc
	events               *inbox.Inbox[conversation.Event]
	ids                  func(prefix string) string
	clock                Clock
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

// WithWebResearcher configures the web researcher. The deep researcher runs
// the same model with the deep research engine in place of nothing else; it is
// available only when WithDeepResearch configures an engine.
func WithWebResearcher(spec agent.Spec) Option {
	return func(s *Session) { s.webResearcher = spec.Clone() }
}
func WithDeepResearcher(spec agent.Spec) Option {
	return func(s *Session) { s.deepResearcher = spec.Clone() }
}

// WithAutoAudit makes the session assign every submitted implementation to a
// fresh auditor itself, instead of notifying the owner to do it: the step
// has nothing to decide, and each cycle cost the manager several model calls.
func WithAutoAudit() Option { return func(s *Session) { s.autoAudit = true } }

// AuditBrief builds what an auditor is handed with its audit of original's
// submission, such as the requirements and the files the implementation
// changed, so it need not gather them again.
type AuditBrief func(original work.Work, submission work.SubmissionID) string

func WithAuditBrief(f AuditBrief) Option { return func(s *Session) { s.auditBrief = f } }

// WithExperimenter configures the experimenter; methods captures the files an
// experiment's method names from the experimenter's copy of the workspace.
func WithExperimenter(spec agent.Spec, methods MethodFiles) Option {
	return func(s *Session) { s.experimenter, s.methodFiles = spec.Clone(), methods }
}

// MethodFiles reads the named files from actor's copy of the workspace.
type MethodFiles func(actor identity.ActorID, paths []string) ([]work.MethodFile, error)

// WithReviewer configures the reviewer, which reads the workspace.
func WithReviewer(spec agent.Spec) Option         { return func(s *Session) { s.reviewer = spec.Clone() } }
func (s *Session) ReviewerSpec() agent.Spec       { return s.reviewer.Clone() }
func (s *Session) ExperimenterSpec() agent.Spec   { return s.experimenter.Clone() }
func (s *Session) WebResearcherSpec() agent.Spec  { return s.webResearcher.Clone() }
func (s *Session) DeepResearcherSpec() agent.Spec { return s.deepResearcher.Clone() }

// WithPublisher replaces the legacy host relay. It acknowledges required work records independently of the dispatcher.
func WithPublisher(p func(conversation.Event) error) Option {
	return func(s *Session) { s.publish = p }
}

func WithAdmission(g *admission.Gate) Option { return func(s *Session) { s.admission = g } }

// WithIDs makes the work store issue ids from next; see work.WithIDs.
func WithIDs(next func(prefix string) string) Option { return func(s *Session) { s.ids = next } }

// Clock is the time the workflow schedules progress notices by. Timer fires
// once the clock reaches at; stop releases it. The default is the wall clock;
// a replay runs a virtual clock that reaches each recorded moment in order.
type Clock interface {
	Now() time.Time
	Timer(at time.Time) (fired <-chan time.Time, stop func())
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) Timer(at time.Time) (<-chan time.Time, func()) {
	t := time.NewTimer(max(time.Until(at), time.Nanosecond))
	return t.C, func() { t.Stop() }
}

// WithClock schedules progress notices by clock instead of the wall clock.
func WithClock(clock Clock) Option {
	return func(s *Session) {
		if clock != nil {
			s.clock = clock
		}
	}
}

func New(ctx context.Context, c *conversation.Controller, implementor, auditor agent.Spec, options ...Option) *Session {
	owner := ctx
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &Session{Controller: c, clock: wallClock{}, implementor: implementor.Clone(), auditor: auditor.Clone(), graph: roster.NewGraph(), progressConfig: DefaultWorkProgressReporting(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	s.interruptDrain = make(chan chan error)
	for _, option := range options {
		option(s)
	}
	storeOptions := []work.Option{work.WithIDs(s.ids)}
	if s.publish != nil {
		storeOptions = append(storeOptions, work.WithEvidenceLookup(s.evidenceLookup), work.WithReporter(work.ReporterFunc(func(_ context.Context, e work.Event) error { return s.publish(conversation.WorkEvent{Event: e}) })))
	}
	s.Store = work.New(storeOptions...)
	if s.publish == nil {
		s.events = inbox.New[conversation.Event]()
	}
	s.implementor.Tools = append(s.withHeldWrites(s.withWorkExecution(s.implementor.Tools)), s.commonTools()...)
	s.implementor.Tools = append(s.implementor.Tools, s.progressTool())
	s.implementor.Tools = append(s.implementor.Tools, s.submitWorkTool())
	s.auditor.Tools = append(s.withWorkExecution(s.auditor.Tools), s.commonTools()...)
	s.auditor.Tools = append(s.auditor.Tools, s.progressTool())
	s.auditor.Tools = append(s.auditor.Tools, tool.Finishing(tool.SubmitAudit(func(ctx context.Context, c tool.Call, r work.AuditRequest) (tool.Result, error) {
		v, e := s.SubmitAudit(ctx, c.Actor, r)
		return result(v, e)
	})))
	if s.webResearcher.Provider != nil {
		s.webResearcher.Tools = append(s.webResearcher.Tools, s.researchTools()...)
	}
	// Only the deep researcher runs the engine, and only it reads the runs:
	// they are its working evidence; everyone else reads the delivered brief
	// and its ledger findings.
	if s.deepResearch == nil {
		s.deepResearcher = agent.Spec{}
	}
	if s.deepResearcher.Provider != nil {
		s.deepResearcher.Tools = append(s.deepResearcher.Tools, s.deepResearchTool())
		if s.researchRead != nil {
			s.deepResearcher.Tools = append(s.deepResearcher.Tools, s.researchRead)
		}
		s.deepResearcher.Tools = append(s.deepResearcher.Tools, s.researchTools()...)
	}
	if s.reviewer.Provider != nil {
		s.reviewer.Tools = append(s.reviewer.Tools, s.researchTools()...)
	}
	if s.experimenter.Provider != nil {
		s.experimenter.Tools = append(s.withWorkExecution(s.experimenter.Tools), s.experimentTools()...)
	}
	s.stopOwner = context.AfterFunc(owner, func() { _ = s.Close(context.Background()) })
	go s.run()
	return s
}

// experimentTools are what the experimenter uses to coordinate, record its
// hypotheses and results, and deliver its conclusion.
func (s *Session) experimentTools() []tool.Tool {
	tools := append(s.commonTools(), s.progressTool(), tool.WaitForInput())
	return append(tools,
		tool.RecordHypothesis(func(ctx context.Context, c tool.Call, r work.RecordHypothesisRequest) (tool.Result, error) {
			v, e := admitted(s, ctx, func(context.Context) (work.HypothesisReceipt, error) { return s.Store.RecordHypothesis(c.Actor, r) })
			return result(v, e)
		}),
		tool.RecordResult(func(ctx context.Context, c tool.Call, r work.RecordResultRequest) (tool.Result, error) {
			v, e := admitted(s, ctx, func(context.Context) (work.HypothesisReceipt, error) { return s.Store.RecordResult(c.Actor, r) })
			return result(v, e)
		}),
		tool.Finishing(tool.SubmitExperiment(func(ctx context.Context, c tool.Call, a tool.SubmitExperimentInput) (tool.Result, error) {
			v, e := s.SubmitExperiment(ctx, c.Actor, a)
			return result(v, e)
		})))
}

// researchTools are what every researcher uses to coordinate, record and
// deliver its research.
func (s *Session) researchTools() []tool.Tool {
	tools := append(s.commonTools(), s.progressTool(), tool.WaitForInput())
	return append(tools, tool.Finishing(tool.SubmitBrief(func(ctx context.Context, c tool.Call, r work.SubmitBriefRequest) (tool.Result, error) {
		v, e := s.SubmitBrief(ctx, c.Actor, r)
		return result(v, e)
	})))
}

// A worker's successful submission ends its exchange: the submission is its
// handoff, and a closing reply only repeated the work record.
func (s *Session) submitWorkTool() tool.Tool {
	return tool.Finishing(tool.SubmitWork(func(ctx context.Context, c tool.Call, r work.SubmitRequest) (tool.Result, error) {
		v, e := s.SubmitWork(ctx, c.Actor, r)
		return result(v, e)
	}))
}

// ResolvedAssignmentReply reports whether m is an agent's reply to an
// assignment that is no longer active for it: submitted, delivered, cancelled
// or reassigned. The ledger already told the owner about that outcome, so the
// reply is a handoff for the record rather than news. Both used to wake the
// owner, and whichever came second cost an exchange that could only end in a
// second "already done" reply.
func (s *Session) ResolvedAssignmentReply(m message.Message) bool {
	if m.Kind != message.Reply || m.ReplyTo == "" {
		return false
	}
	s.mu.Lock()
	b, ok := s.assignments[m.ReplyTo]
	s.mu.Unlock()
	return ok && b.recipient == m.From && !s.current(b)
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

// CoordinationTools are the manager's planning, staffing and assignment tools.
func (s *Session) CoordinationTools() []tool.Tool {
	tools := append(s.commonTools(),
		tool.WaitForInput(),
		tool.CreateAgent(func(ctx context.Context, c tool.Call, r roster.CreateRequest) (tool.Result, error) {
			v, e := s.CreateAgent(ctx, c.Actor, r)
			return result(v, e)
		}, !s.autoAudit))
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
	_, err := s.Controller.Deliver(b.owner, message.Draft{To: b.owner, Kind: message.Notification, Content: fmt.Sprintf("Work %s delivery/execution needs attention for assignee %s: %s. Inspect the work, then cancel it and assign it again; this is not an audit verdict.", b.work, b.recipient, detail)})
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
	audited := map[work.EventID]bool{}
	published := map[work.EventID]bool{}
	revoked := map[work.EventID]bool{}
	forget := func(id work.EventID) {
		delete(attempted, id)
		delete(published, id)
		delete(revoked, id)
	}
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
	var timerC <-chan time.Time
	stopTimer := func() {}
	defer func() { stopTimer() }()
	resetTimer := func() {
		stopTimer()
		timerC, stopTimer = nil, func() {}
		if due := queue.next(); !due.IsZero() {
			timerC, stopTimer = s.clock.Timer(due)
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
			// Hypotheses and their results are the experimenter's working
			// record; the owner hears about the experiment when it delivers.
			if e.Kind == work.HypothesisRecorded || e.Kind == work.HypothesisResolved {
				_ = s.Store.AcknowledgeEvent(e.ID)
				forget(e.ID)
				continue
			}
			if e.Kind == work.WorkProgressReported || e.Kind == work.BriefDelivered {
				if attempted[e.ID] {
					continue
				}
				attempted[e.ID] = true
				if e.Kind == work.BriefDelivered {
					sendNotice(w.Owner, message.WorkProgressNotice{Covered: coverages[e.ID], Briefs: []message.BriefRef{{WorkID: w.ID, AssignedAtRevision: w.AssignedAtRevision, WorkRevision: w.Revision, BriefID: w.LatestBriefID}}, Attention: true}, []work.EventID{e.ID})
				} else if e.Actionable {
					sendNotice(w.Owner, message.WorkProgressNotice{Reports: []message.ProgressReportRef{{WorkID: w.ID, AssignedAtRevision: w.AssignedAtRevision, WorkRevision: w.Revision, ReportID: w.LatestProgressReportID}}, Attention: true}, []work.EventID{e.ID})
				} else if e.Change != nil && len(e.Change.ProgressReports) > 0 && len(e.Change.ProgressReports[0].Findings) > 0 {
					queue.add(e, s.clock.Now())
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
			if e.Kind == work.ReviewRequested && s.autoAudit && !audited[e.ID] {
				audited[e.ID] = true
				if err := s.assignAudit(e.Work); err == nil {
					_ = s.Store.AcknowledgeEvent(e.ID)
					forget(e.ID)
					continue
				} else if !errors.Is(err, conversation.ErrInterrupted) {
					// The owner hears of the submission as before and the
					// user sees why no audit started.
					s.emit(conversation.MessageEvent{Message: message.Message{To: message.User, Kind: message.Failure, Content: fmt.Sprintf("Could not assign an audit for %s: %v", w.ID, err)}})
				}
			}
			assignment := e.Kind == work.WorkAssigned || e.Kind == work.WorkReassigned
			if assignment {
				b := binding{w.ID, w.AssignedAtRevision, w.Assignee, w.Owner}
				if !s.current(b) {
					_ = s.Store.AcknowledgeEvent(e.ID)
					forget(e.ID)
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
				s.mu.Lock()
				if s.assignments == nil {
					s.assignments = map[message.MessageID]binding{}
				}
				s.assignments[receipt.MessageID] = b
				s.mu.Unlock()
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
			forget(e.ID)
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
							forget(id)
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
