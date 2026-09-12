// Package agent owns the sequential inbox/model/tool loop. Root and delegated
// agents use the same implementation, with different instructions and tools.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

type Spec struct {
	Provider provider.Provider
	Prompt   prompt.Prompt
	Tools    []tool.Tool
}

// Clone snapshots the prompt and tool list. Provider and tool implementations
// remain shared collaborators and must support concurrent use.
func (s Spec) Clone() Spec {
	s.Prompt = s.Prompt.Clone()
	s.Tools = append([]tool.Tool(nil), s.Tools...)
	return s
}

// Config supplies the collaborators owned by the conversation controller.
type Config struct {
	Reporter   Reporter // Required for recoverable hosts; nil is an explicitly unrecorded low-level agent.
	ID         message.ActorID
	ReplyTo    message.ActorID
	Spec       Spec
	Inbox      *inbox.Inbox[message.Message]
	Outbox     message.Sender
	OnConsumed func(message.Receipt)
	// OnState enqueues state notifications; it must not block or reenter this agent.
	OnState     func(State)
	OnLifecycle func(StateSnapshot) // Ordered state and revision from the same lifecycle lock.
	// OnCommentary enqueues assistant text accompanying a tool batch.
	// It must not block on a consumer or reenter this agent.
	OnCommentary func(string)
	// OnTool enqueues execution notifications; it must not block on a consumer.
	OnTool func(ToolActivity)
	// OnToolBatch enqueues a complete tool batch's history boundary. It must
	// not block on a consumer; counting is a separate host operation.
	OnToolBatch func(ToolBatch)
	// OnUsage enqueues one observation after each Submit returns, even on error.
	// It must not block on a consumer. The observation owns its counts.
	OnUsage func(UsageObservation)
}

type Agent struct {
	emission       sync.Mutex
	reporting      reportState
	stopRequested  atomic.Bool
	nextOutput     uint64
	nextInvocation uint64
	config         Config
	tools          map[string]tool.Tool
	definitions    []provider.ToolDefinition
	thread         thread
	usage          usageTracker
	started        atomic.Bool
	control        lifecycle
}

func New(config Config) (*Agent, error) {
	if config.Spec.Provider == nil || config.Inbox == nil || config.Outbox == nil {
		return nil, errors.New("agent requires a provider, inbox, and outbox")
	}
	config.Spec = config.Spec.Clone()
	a := &Agent{config: config, tools: make(map[string]tool.Tool), control: lifecycle{state: Idle, revision: 1, changed: make(chan struct{})}}
	for _, t := range config.Spec.Tools {
		if t == nil {
			return nil, errors.New("nil tool")
		}
		if checked, ok := t.(interface{ Validate() error }); ok {
			if err := checked.Validate(); err != nil {
				return nil, fmt.Errorf("invalid tool: %w", err)
			}
		}
		definition := t.Definition()
		if definition.Name == "" {
			return nil, errors.New("tool has no name")
		}
		if _, exists := a.tools[definition.Name]; exists {
			return nil, fmt.Errorf("duplicate tool: %s", definition.Name)
		}
		a.tools[definition.Name] = t
		definition.Parameters = append(json.RawMessage(nil), definition.Parameters...)
		a.definitions = append(a.definitions, definition)
	}
	system, err := config.Spec.Prompt.Render()
	if err != nil {
		return nil, fmt.Errorf("render agent prompt: %w", err)
	}
	a.thread.append(provider.Message{Role: "system", Content: content.Text(system)})
	return a, nil
}

// Run starts exactly one loop. A text response ends an exchange, not the agent.
// Each tool batch settles before inbox input is consumed and another model call
// begins. There is deliberately no revision validation or proposal loop here.
func (a *Agent) Run(ctx context.Context) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.reporting.mu.Lock()
	a.reporting.cancel = cancel
	a.reporting.mu.Unlock()
	var last message.MessageID
	if !a.started.CompareAndSwap(false, true) {
		return errors.New("agent already started")
	}
	defer func() {
		a.emission.Lock()
		defer a.emission.Unlock()
		a.control.mu.Lock()
		state := Stopped
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			state = Failed
		}
		a.setStateLocked(state)
		a.unlockAndReportState()
		err = errors.Join(err, a.reportError())
	}()
	initial, _ := a.thread.requestMessages()
	if err := a.report(HistoryAppended{Position: 1, Message: initial[0]}); err != nil {
		return err
	}
	for {
		if err := a.waitInbox(ctx); err != nil {
			return err
		}
		incoming, err := a.config.Inbox.Receive(ctx)
		if err != nil {
			return err
		}
		if incoming.Kind != message.Notification && incoming.Kind != message.Observation {
			last = incoming.ID
		}
		if err := a.consume(incoming); err != nil {
			return err
		}
		if incoming.Kind == message.Observation {
			continue
		}
		for {
			if err := a.checkpoint(ctx); err != nil {
				return err
			}
			for _, incoming := range a.config.Inbox.Drain() {
				if incoming.Kind != message.Notification && incoming.Kind != message.Observation {
					last = incoming.ID
				}
				if err := a.consume(incoming); err != nil {
					return err
				}
			}
			if err := a.checkpoint(ctx); err != nil {
				return err
			}
			request, revision := a.request()
			response, output, err := a.generate(ctx, request, revision)
			if err != nil {
				return err
			}
			if err := a.checkpoint(ctx); err != nil {
				return err
			}
			if len(response.ToolCalls) == 0 {
				if response.Content == "" {
					return errors.New("model returned no text or tool calls")
				}
				_, err := a.config.Outbox.Send(ctx, message.Draft{
					To: a.config.ReplyTo, Kind: message.Reply, ReplyTo: last, Content: response.Content, Output: &output,
				})
				if err != nil {
					return err
				}
				break
			}
			if strings.TrimSpace(response.Content) != "" {
				if err := a.report(Commentary{Output: output, Text: response.Content}); err != nil {
					return err
				}
				if a.config.OnCommentary != nil {
					a.config.OnCommentary(response.Content)
				}
			}
			var toolRevision uint64
			for _, call := range response.ToolCalls {
				if err := a.checkpoint(ctx); err != nil {
					return err
				}
				result, err := a.call(ctx, call)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					result = tool.Text("Tool error: " + err.Error())
				}
				toolRevision, err = a.appendHistory(provider.Message{
					Role: "tool", Content: result.Content.Clone(), ToolCallID: call.ID,
				}, nil)
				if err != nil {
					return err
				}
			}
			{
				calls := make([]string, len(response.ToolCalls))
				for i, call := range response.ToolCalls {
					calls[i] = call.ID
				}
				batch := ToolBatch{Calls: calls, ContextRevision: toolRevision}
				if err := a.report(batch); err != nil {
					return err
				}
				if a.config.OnToolBatch != nil {
					a.config.OnToolBatch(batch)
				}
			}
		}
	}
}

func (a *Agent) consume(incoming message.Message) error {
	// Attribution must reach actual providers, not only test metadata.
	encoded, _ := json.Marshal(incoming)
	_, err := a.appendHistory(provider.Message{
		Role: "user", Content: content.Text(string(encoded)), Envelope: &incoming,
	}, nil)
	if err != nil {
		return err
	}
	if err := a.report(Consumed{Receipt: message.Receipt{MessageID: incoming.ID, Recipient: a.config.ID, Status: message.Consumed}}); err != nil {
		return err
	}
	if a.config.OnConsumed != nil {
		a.config.OnConsumed(message.Receipt{
			MessageID: incoming.ID, Recipient: a.config.ID, Status: message.Consumed,
		})
	}
	return nil
}

func (a *Agent) request() (provider.Request, uint64) {
	definitions := append([]provider.ToolDefinition(nil), a.definitions...)
	for i := range definitions {
		definitions[i].Parameters = append(json.RawMessage(nil), definitions[i].Parameters...)
	}
	messages, revision := a.thread.requestMessages()
	return provider.Request{
		Agent: a.config.ID, Messages: messages, Tools: definitions,
	}, revision
}

func (a *Agent) call(ctx context.Context, call provider.ToolCall) (result tool.Result, err error) {
	started := time.Now()
	a.nextInvocation++
	invocation := fmt.Sprintf("%s/tool-%d", a.config.ID, a.nextInvocation)
	if err := a.reportTool(ToolActivity{InvocationID: invocation, Call: call, StartedAt: started}); err != nil {
		return tool.Result{}, err
	}
	defer func() {
		observedErr := err
		if observedErr == nil {
			observedErr = ctx.Err()
		}
		err = errors.Join(err, a.reportTool(ToolActivity{InvocationID: invocation, Diagnostic: tool.DiagnosticFrom(observedErr), Call: call, StartedAt: started, FinishedAt: time.Now(), Result: result, Err: observedErr}))
	}()
	t, ok := a.tools[call.Name]
	if !ok {
		return tool.Result{}, fmt.Errorf("unknown tool: %s", call.Name)
	}
	return t.Call(ctx, tool.Call{
		Arguments: append(json.RawMessage(nil), call.Arguments...),
		Actor:     a.config.ID,
		Sender:    a.config.Outbox,
	})
}
