// Package conversation owns one persistent root, its agents, and message
// routing. It performs no model or tool work while holding its coordination lock.
package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
)

var ErrClosed = errors.New("conversation closed")
var ErrAgentNotFound = errors.New("unknown agent")
var ErrAgentStopped = errors.New("agent stopped")

type ownedAgent struct {
	info   AgentInfo
	agent  *agent.Agent
	inbox  *inbox.Inbox[message.Message]
	ctx    context.Context
	cancel context.CancelFunc
}

type Controller struct {
	interrupted  bool
	interruption *interruptAttempt
	admitInbox   func(message.ActorID) agent.InboxAdmission
	wakeContext  func(message.ActorID) agent.WakeContext
	emission     sync.Mutex
	reporter     Reporter
	source       func(context.Context) (Event, error)
	mu           sync.Mutex
	closing      bool
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	quiesce      chan struct{}
	quiesceOnce  sync.Once
	wg           sync.WaitGroup
	root         message.ActorID
	agents       map[message.ActorID]*ownedAgent
	order        []message.ActorID
	receipts     map[message.MessageID]message.Receipt
	events       *inbox.Inbox[Event]
	nextAgent    uint64
	nextMessage  uint64
}

// New creates an empty conversation. CreateAgent(message.User, spec) establishes
// its root. The application supplies every agent's prompt and tools explicitly.
type Reporter interface {
	Publish(context.Context, Event) error
}
type ReporterFunc func(context.Context, Event) error

func (f ReporterFunc) Publish(ctx context.Context, e Event) error { return f(ctx, e) }

type Option func(*Controller)

func WithReporting(r Reporter, source func(context.Context) (Event, error)) Option {
	return func(c *Controller) { c.reporter = r; c.source = source; c.events = nil }
}
func WithInboxAdmission(f func(message.ActorID) agent.InboxAdmission) Option {
	return func(c *Controller) { c.admitInbox = f }
}

// WithWakeContext supplies per-agent wake context; see agent.WakeContext.
func WithWakeContext(f func(message.ActorID) agent.WakeContext) Option {
	return func(c *Controller) { c.wakeContext = f }
}
func New(ctx context.Context, options ...Option) *Controller {
	ctx, cancel := context.WithCancel(ctx)
	c := &Controller{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), quiesce: make(chan struct{}),
		agents:   make(map[message.ActorID]*ownedAgent),
		receipts: make(map[message.MessageID]message.Receipt),
		events:   inbox.New[Event](),
	}
	for _, o := range options {
		o(c)
	}
	go func() {
		// Shutdown is either requested or forced. A requested shutdown lets each
		// loop finish what it is doing and exit without an error to report; a
		// cancelled context is an abort, and the cancellation it produces is a
		// fact worth keeping. Close escalates the first into the second when a
		// caller's budget runs out.
		requested := false
		select {
		case <-ctx.Done():
		case <-c.quiesce:
			requested = true
		}
		// Close admission under the same lock as CreateAgent's Add. Once
		// released, all admitted agents are registered and no more can join.
		c.mu.Lock()
		c.closing = true
		ownedAgents := make([]*ownedAgent, 0, len(c.agents))
		for _, owned := range c.agents {
			ownedAgents = append(ownedAgents, owned)
		}
		c.mu.Unlock()
		for _, owned := range ownedAgents {
			if requested {
				owned.agent.RequestQuiesce()
				continue
			}
			owned.cancel()
			owned.agent.RequestStop()
		}
		c.wg.Wait()
		if c.events != nil {
			c.events.Close()
		}
		close(c.done)
	}()
	return c
}

// Root returns the conversation entry agent, whose parent is the user.
// It is empty until the first successful creation with message.User as parent.
// Its identity remains stable even after that agent stops.
func (c *Controller) Root() message.ActorID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.root
}

// CreateAgent starts an idle agent. Send a message separately to give it work.
// The first agent has message.User as its parent and becomes the root.
// Subsequent parents must be active agents. The parent receives text responses
// and model failures. The supplied spec is used without tool or prompt injection.
func (c *Controller) CreateAgent(parent message.ActorID, spec agent.Spec) (Creation, error) {
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.createLocked(parent, spec)
}

func (c *Controller) createLocked(parent message.ActorID, spec agent.Spec) (Creation, error) {
	if c.closedLocked() {
		return Creation{}, ErrClosed
	}
	if c.interrupted {
		return Creation{}, ErrInterrupted
	}
	if parent == message.User {
		if c.root != "" {
			return Creation{}, errors.New("conversation already has a root agent")
		}
	} else if err := c.activeLocked(parent); err != nil {
		return Creation{}, err
	}
	c.nextAgent++
	id := message.ActorID(fmt.Sprintf("agent-%d", c.nextAgent))
	ctx, cancel := context.WithCancel(c.ctx)
	mail := inbox.New[message.Message]()
	var admit agent.InboxAdmission
	if c.admitInbox != nil {
		admit = c.admitInbox(id)
	}
	var wake agent.WakeContext
	if c.wakeContext != nil {
		wake = c.wakeContext(id)
	}
	runner, err := agent.New(agent.Config{AdmitInbox: admit, WakeContext: wake,
		ID: id, ReplyTo: parent, Spec: spec, Inbox: mail,
		Outbox: sender{controller: c, actor: id},
		Reporter: agent.ReporterFunc(func(ctx context.Context, e agent.Event) error {
			switch v := e.(type) {
			case agent.Consumed:
				return c.acknowledge(v.Receipt)
			case agent.StateSnapshot:
				return c.emit(AgentStateChanged{Agent: id, State: v.State, Revision: v.Revision})
			case agent.Commentary:
				return c.emit(CommentaryEvent{Agent: id, Content: v.Text, Output: &v.Output})
			case agent.ToolActivity:
				return c.emit(ToolEvent{Agent: id, Activity: v})
			case agent.ToolBatch:
				return c.emit(ToolBatchEvent{Agent: id, Batch: v})
			case agent.UsageObservation:
				return c.emit(UsageEvent{Agent: id, Observation: v})
			default:
				return c.emit(AgentEvent{Agent: id, Event: e})
			}
		}),
	})
	if err != nil {
		cancel()
		return Creation{}, err
	}
	owned := &ownedAgent{
		info: AgentInfo{ID: id, Parent: parent, State: agent.Idle, StateRevision: 1}, agent: runner, inbox: mail, ctx: ctx, cancel: cancel,
	}
	if parent == message.User {
		c.root = id
	}
	c.agents[id] = owned
	c.order = append(c.order, id)
	if err := c.emitLocked(AgentStarted{Agent: owned.info, Tools: runner.Definitions(), OutputTokenLimit: runner.OutputTokenLimit()}); err != nil {
		cancel()
		return Creation{}, err
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := runner.Run(ctx)
		c.finish(owned, err)
	}()
	return Creation{AgentID: id}, nil
}

// Send delivers user input. Model-facing senders are bound to their own actor.
func (c *Controller) Send(to message.ActorID, content string) (message.Receipt, error) {
	return c.SendContext(context.Background(), to, content)
}

// SendContext checks caller cancellation before admitting a delivery. Publication
// is not rollback: an already admitted delivery may finish after cancellation.
func (c *Controller) SendContext(ctx context.Context, to message.ActorID, content string) (message.Receipt, error) {
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return message.Receipt{}, err
	}
	if c.closedLocked() {
		return message.Receipt{}, ErrClosed
	}
	if c.interruptionPendingLocked() {
		return message.Receipt{}, ErrInterrupted
	}
	draft := message.Draft{To: to, Kind: message.Instruction, Content: content}
	if err := draft.Validate(); err != nil {
		return message.Receipt{}, err
	}
	if to != message.User {
		if err := c.activeLocked(to); err != nil {
			return message.Receipt{}, err
		}
	}
	// Retire the previous completed interruption before publication releases mu.
	// A Stop racing publication must create a fresh cancellation fence, and an
	// admitted Send must never release that newer fence when it returns.
	c.releaseInterruptionLocked()
	return c.deliverLocked(message.User, draft)
}

// Deliver is a host operation for application-owned work dispatch. Model tools
// continue to use their bound Sender; they cannot supply a sender identity.
func (c *Controller) Deliver(from message.ActorID, draft message.Draft) (message.Receipt, error) {
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.interrupted {
		return message.Receipt{}, ErrInterrupted
	}
	if from != message.User {
		if _, ok := c.agents[from]; !ok {
			return message.Receipt{}, fmt.Errorf("unknown sender: %s", from)
		}
	}
	return c.sendLocked(from, draft)
}

func (c *Controller) sendLocked(from message.ActorID, draft message.Draft) (message.Receipt, error) {
	if c.closedLocked() {
		return message.Receipt{}, ErrClosed
	}
	if err := draft.Validate(); err != nil {
		return message.Receipt{}, err
	}
	if draft.To != message.User {
		if err := c.activeLocked(draft.To); err != nil {
			return message.Receipt{}, err
		}
	}
	return c.deliverLocked(from, draft)
}

func (c *Controller) deliverLocked(from message.ActorID, draft message.Draft) (message.Receipt, error) {
	c.nextMessage++
	m := message.Message{
		ID:   message.MessageID(fmt.Sprintf("message-%d", c.nextMessage)),
		From: from, To: draft.To, Kind: draft.Kind, ReplyTo: draft.ReplyTo, Content: draft.Content,
		Work: draft.Work, Event: draft.Event, Output: draft.Output, Progress: draft.Progress,
	}
	m = m.Clone()
	r := message.Receipt{MessageID: m.ID, Recipient: m.To, Status: message.Queued}
	c.receipts[m.ID] = r
	if err := c.emitLocked(MessageEvent{Message: m.Clone()}); err != nil {
		return r, err
	}
	if err := c.emitLocked(AckEvent{Receipt: r}); err != nil {
		return r, err
	}
	if m.To != message.User {
		_ = c.agents[m.To].inbox.Send(m)
	}
	return r, nil
}

func (c *Controller) activeLocked(id message.ActorID) error {
	a, ok := c.agents[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	if a.agent.State().Terminal() || a.ctx.Err() != nil {
		return fmt.Errorf("%w: %s", ErrAgentStopped, id)
	}
	return nil
}

func (c *Controller) acknowledge(r message.Receipt) error {
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receipts[r.MessageID] = r
	return c.emitLocked(AckEvent{Receipt: r})
}

func (c *Controller) finish(a *ownedAgent, err error) {
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	a.cancel()
	a.inbox.Close()
	for _, m := range a.inbox.Drain() {
		r := message.Receipt{
			MessageID: m.ID, Recipient: m.To, Status: message.Undelivered,
			Detail: "Agent stopped before consuming this message",
		}
		c.receipts[m.ID] = r
		c.emitLocked(AckEvent{Receipt: r})
	}
	_ = c.emitLocked(AgentExited{Agent: a.info.ID, Err: err})
	if err != nil && !errors.Is(err, context.Canceled) && c.ctx.Err() == nil {
		_, _ = c.sendLocked(a.info.ID, message.Draft{
			To: a.info.Parent, Kind: message.Failure,
			Content: fmt.Sprintf("Agent %s failed: %v", a.info.ID, err),
		})
	}
}

// Receipt reports the latest delivery milestone. It does not infer acceptance or
// completion. User-directed messages remain queued; the host owns their display.
func (c *Controller) Receipt(id message.MessageID) (message.Receipt, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.receipts[id]
	return r, ok
}

func (c *Controller) Agents() []AgentInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]AgentInfo, 0, len(c.order))
	for _, id := range c.order {
		info := c.agents[id].info
		state := c.agents[id].agent.StateSnapshot()
		info.State, info.StateRevision = state.State, state.Revision
		result = append(result, info)
	}
	return result
}

// NextEvent is a single-consumer host stream. It drains after Close, then returns
// inbox.ErrClosed. Agents communicate through routed messages, not this stream.
func (c *Controller) NextEvent(ctx context.Context) (Event, error) {
	if c.source != nil {
		return c.source(ctx)
	}
	return c.events.Receive(ctx)
}

// InspectOptions requests an optional page of the agent's actual thread.
type InspectOptions struct {
	Transcript *agent.TranscriptQuery
}

type AgentInspection struct {
	AgentInfo
	ContextRevision  uint64                `json:"context_revision"`
	OutputTokenLimit *int64                `json:"output_token_limit,omitempty"`
	Usage            agent.UsageSnapshot   `json:"usage"`
	Transcript       *agent.TranscriptPage `json:"transcript,omitempty"`
}

// InspectAgent returns state, usage, and an optional independent transcript.
// These snapshots do not form an atomic execution checkpoint.
func (c *Controller) InspectAgent(id message.ActorID, options InspectOptions) (AgentInspection, error) {
	c.mu.Lock()
	owned, ok := c.agents[id]
	if !ok {
		c.mu.Unlock()
		return AgentInspection{}, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	info, runner := owned.info, owned.agent
	c.mu.Unlock()
	state := runner.StateSnapshot()
	info.State, info.StateRevision = state.State, state.Revision
	inspection := AgentInspection{AgentInfo: info, Usage: runner.Usage(), ContextRevision: runner.ContextRevision(), OutputTokenLimit: runner.OutputTokenLimit()}
	if options.Transcript != nil {
		page, err := runner.Transcript(*options.Transcript)
		if err != nil {
			return AgentInspection{}, err
		}
		inspection.Transcript = &page
	}
	return inspection, nil
}

// CountAgentTokens counts the exact history boundary in a ToolBatchEvent. No
// coordination lock is held during network I/O; ctx controls the host request.
func (c *Controller) CountAgentTokens(ctx context.Context, id message.ActorID, revision uint64) (int64, error) {
	c.mu.Lock()
	owned, ok := c.agents[id]
	c.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	return owned.agent.CountTokens(ctx, revision)
}

func (c *Controller) PauseAgent(id message.ActorID) (AgentInfo, error) {
	c.mu.Lock()
	if err := c.activeLocked(id); err != nil {
		c.mu.Unlock()
		return AgentInfo{}, err
	}
	owned := c.agents[id]
	c.mu.Unlock()
	state, err := owned.agent.PauseSnapshot()
	info := owned.info
	info.State, info.StateRevision = state.State, state.Revision
	return info, err
}

func (c *Controller) ResumeAgent(id message.ActorID) (AgentInfo, error) {
	c.mu.Lock()
	if err := c.activeLocked(id); err != nil {
		c.mu.Unlock()
		return AgentInfo{}, err
	}
	owned := c.agents[id]
	c.mu.Unlock()
	state, err := owned.agent.ResumeSnapshot()
	info := owned.info
	info.State, info.StateRevision = state.State, state.Revision
	return info, err
}

func (c *Controller) StopAgent(id message.ActorID) (AgentInfo, error) {
	c.mu.Lock()
	owned, ok := c.agents[id]
	if !ok {
		c.mu.Unlock()
		return AgentInfo{}, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	c.mu.Unlock()
	owned.cancel()
	state, err := owned.agent.RequestStopSnapshot()
	info := owned.info
	info.State, info.StateRevision = state.State, state.Revision
	return info, err
}

// Close asks every owned agent to stop at its next safe point and waits for the
// loops to exit. Work already in flight finishes, so a clean shutdown reports no
// cancellation. When ctx expires first the request escalates to cancellation,
// which is cooperative: a model or tool that ignores its context can outlast the
// wait. A timeout leaves ownership intact; Close may be called again to finish
// waiting, and the escalation is not undone.
func (c *Controller) Close(ctx context.Context) error {
	c.quiesceOnce.Do(func() { close(c.quiesce) })
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
	}
	// The request did not settle within the caller's budget. Escalate to
	// cancellation and give the loops a moment to unwind, so a caller that
	// budgeted for a close still gets one rather than only an error.
	c.cancel()
	grace := time.NewTimer(escalationGrace)
	defer grace.Stop()
	select {
	case <-c.done:
		return nil
	case <-grace.C:
		return ctx.Err()
	}
}

// escalationGrace bounds the wait after a close escalates to cancellation.
// Cancellation is cooperative, so a model or tool that ignores its context can
// still outlast it; the caller then sees its own deadline.
const escalationGrace = time.Second

// closedLocked reports that the conversation no longer admits work. A close
// that settles gracefully never cancels c.ctx, so the closing flag and not the
// context is what makes a conversation closed.
func (c *Controller) closedLocked() bool { return c.closing || c.ctx.Err() != nil }

func (c *Controller) emit(event Event) error {
	if c.reporter != nil {
		if err := c.reporter.Publish(context.Background(), event); err != nil {
			c.cancel()
			return err
		}
		return nil
	}
	return c.events.Send(event)
}

// emitLocked is called with emission and mu. Only emission spans the I/O wait.
func (c *Controller) emitLocked(e Event) error {
	c.mu.Unlock()
	err := c.emit(e)
	c.mu.Lock()
	return err
}
func (c *Controller) Done() <-chan struct{} { return c.done }

type sender struct {
	controller *Controller
	actor      message.ActorID
}

func (s sender) Send(ctx context.Context, draft message.Draft) (message.Receipt, error) {
	if draft.Progress != nil {
		return message.Receipt{}, errors.New("progress notices require host delivery")
	}
	c := s.controller
	c.emission.Lock()
	defer c.emission.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return message.Receipt{}, err
	}
	if c.interrupted {
		return message.Receipt{}, ErrInterrupted
	}
	if err := c.activeLocked(s.actor); err != nil {
		return message.Receipt{}, err
	}
	return c.sendLocked(s.actor, draft)
}
