// Package conversation owns one persistent root, its agents, and message
// routing. It performs no model or tool work while holding its coordination lock.
package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
)

var ErrClosed = errors.New("conversation closed")

type ownedAgent struct {
	info   AgentInfo
	agent  *agent.Agent
	inbox  *inbox.Inbox[message.Message]
	ctx    context.Context
	cancel context.CancelFunc
}

type Controller struct {
	mu          sync.Mutex
	closing     bool
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	wg          sync.WaitGroup
	root        message.ActorID
	agents      map[message.ActorID]*ownedAgent
	order       []message.ActorID
	receipts    map[message.MessageID]message.Receipt
	events      *inbox.Inbox[Event]
	nextAgent   uint64
	nextMessage uint64
}

// New creates an empty conversation. CreateAgent(message.User, spec) establishes
// its root. The application supplies every agent's prompt and tools explicitly.
func New(ctx context.Context) *Controller {
	ctx, cancel := context.WithCancel(ctx)
	c := &Controller{
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		agents:   make(map[message.ActorID]*ownedAgent),
		receipts: make(map[message.MessageID]message.Receipt),
		events:   inbox.New[Event](),
	}
	go func() {
		<-ctx.Done()
		// Close admission under the same lock as CreateAgent's Add. Once
		// released, all admitted agents are registered and no more can join.
		c.mu.Lock()
		c.closing = true
		for _, owned := range c.agents {
			owned.agent.RequestStop()
		}
		c.mu.Unlock()
		c.wg.Wait()
		c.events.Close()
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
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.createLocked(parent, spec)
}

func (c *Controller) createLocked(parent message.ActorID, spec agent.Spec) (Creation, error) {
	if c.closing || c.ctx.Err() != nil {
		return Creation{}, ErrClosed
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
	runner, err := agent.New(agent.Config{
		ID: id, ReplyTo: parent, Spec: spec, Inbox: mail,
		Outbox: sender{controller: c, actor: id}, OnConsumed: c.acknowledge,
		OnState:     func(state agent.State) { c.emit(AgentStateChanged{Agent: id, State: state}) },
		OnTool:      func(activity agent.ToolActivity) { c.emit(ToolEvent{Agent: id, Activity: activity}) },
		OnToolBatch: func(batch agent.ToolBatch) { c.emit(ToolBatchEvent{Agent: id, Batch: batch}) },
		OnUsage:     func(observation agent.UsageObservation) { c.emit(UsageEvent{Agent: id, Observation: observation}) },
	})
	if err != nil {
		cancel()
		return Creation{}, err
	}
	owned := &ownedAgent{
		info: AgentInfo{ID: id, Parent: parent, State: agent.Idle}, agent: runner, inbox: mail, ctx: ctx, cancel: cancel,
	}
	if parent == message.User {
		c.root = id
	}
	c.agents[id] = owned
	c.order = append(c.order, id)
	c.emit(AgentStarted{Agent: owned.info})
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
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendLocked(message.User, message.Draft{To: to, Kind: message.Instruction, Content: content})
}

// Deliver is a host operation for application-owned work dispatch. Model tools
// continue to use their bound Sender; they cannot supply a sender identity.
func (c *Controller) Deliver(from message.ActorID, draft message.Draft) (message.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if from != message.User {
		if _, ok := c.agents[from]; !ok {
			return message.Receipt{}, fmt.Errorf("unknown sender: %s", from)
		}
	}
	return c.sendLocked(from, draft)
}

func (c *Controller) sendLocked(from message.ActorID, draft message.Draft) (message.Receipt, error) {
	if c.ctx.Err() != nil {
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
	return c.deliverLocked(from, draft), nil
}

func (c *Controller) deliverLocked(from message.ActorID, draft message.Draft) message.Receipt {
	c.nextMessage++
	m := message.Message{
		ID:   message.MessageID(fmt.Sprintf("message-%d", c.nextMessage)),
		From: from, To: draft.To, Kind: draft.Kind, ReplyTo: draft.ReplyTo, Content: draft.Content,
		Work: draft.Work, Event: draft.Event,
	}
	m = m.Clone()
	r := message.Receipt{MessageID: m.ID, Recipient: m.To, Status: message.Queued}
	c.receipts[m.ID] = r
	c.emit(MessageEvent{Message: m.Clone()})
	c.emit(AckEvent{Receipt: r})
	if m.To != message.User {
		_ = c.agents[m.To].inbox.Send(m)
	}
	return r
}

func (c *Controller) activeLocked(id message.ActorID) error {
	a, ok := c.agents[id]
	if !ok {
		return fmt.Errorf("unknown agent: %s", id)
	}
	if a.agent.State().Terminal() || a.ctx.Err() != nil {
		return fmt.Errorf("agent stopped: %s", id)
	}
	return nil
}

func (c *Controller) acknowledge(r message.Receipt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receipts[r.MessageID] = r
	c.emit(AckEvent{Receipt: r})
}

func (c *Controller) finish(a *ownedAgent, err error) {
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
		c.emit(AckEvent{Receipt: r})
	}
	c.emit(AgentExited{Agent: a.info.ID, Err: err})
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
		info.State = c.agents[id].agent.State()
		result = append(result, info)
	}
	return result
}

// NextEvent is a single-consumer host stream. It drains after Close, then returns
// inbox.ErrClosed. Agents communicate through routed messages, not this stream.
func (c *Controller) NextEvent(ctx context.Context) (Event, error) {
	return c.events.Receive(ctx)
}

// InspectOptions requests an optional page of the agent's actual thread.
type InspectOptions struct {
	Transcript *agent.TranscriptQuery
}

type AgentInspection struct {
	AgentInfo
	Usage      agent.UsageSnapshot   `json:"usage"`
	Transcript *agent.TranscriptPage `json:"transcript,omitempty"`
}

// InspectAgent returns state, usage, and an optional independent transcript.
// These snapshots do not form an atomic execution checkpoint.
func (c *Controller) InspectAgent(id message.ActorID, options InspectOptions) (AgentInspection, error) {
	c.mu.Lock()
	owned, ok := c.agents[id]
	if !ok {
		c.mu.Unlock()
		return AgentInspection{}, fmt.Errorf("unknown agent: %s", id)
	}
	info, runner := owned.info, owned.agent
	c.mu.Unlock()
	info.State = runner.State()
	inspection := AgentInspection{AgentInfo: info, Usage: runner.Usage()}
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
		return 0, fmt.Errorf("unknown agent: %s", id)
	}
	return owned.agent.CountTokens(ctx, revision)
}

func (c *Controller) PauseAgent(id message.ActorID) (AgentInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.activeLocked(id); err != nil {
		return AgentInfo{}, err
	}
	owned := c.agents[id]
	state, err := owned.agent.Pause()
	info := owned.info
	info.State = state
	return info, err
}

func (c *Controller) ResumeAgent(id message.ActorID) (AgentInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.activeLocked(id); err != nil {
		return AgentInfo{}, err
	}
	owned := c.agents[id]
	state, err := owned.agent.Resume()
	info := owned.info
	info.State = state
	return info, err
}

func (c *Controller) StopAgent(id message.ActorID) (AgentInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	owned, ok := c.agents[id]
	if !ok {
		return AgentInfo{}, fmt.Errorf("unknown agent: %s", id)
	}
	state := owned.agent.RequestStop()
	owned.cancel()
	info := owned.info
	info.State = state
	return info, nil
}

// Close cancels every owned agent and waits for its loop to exit. Cancellation is
// cooperative: a model or tool that ignores its context can outlast this wait.
// A timeout leaves ownership intact; Close may be called again to finish waiting.
func (c *Controller) Close(ctx context.Context) error {
	c.cancel()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Controller) emit(event Event) { _ = c.events.Send(event) }

type sender struct {
	controller *Controller
	actor      message.ActorID
}

func (s sender) Send(ctx context.Context, draft message.Draft) (message.Receipt, error) {
	c := s.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return message.Receipt{}, err
	}
	if err := c.activeLocked(s.actor); err != nil {
		return message.Receipt{}, err
	}
	return c.sendLocked(s.actor, draft)
}
