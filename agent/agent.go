// Package agent owns the sequential inbox/model/tool loop. Root and delegated
// agents use the same implementation, with different instructions and tools.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// ReasoningLimit is the number of reasoning bytes one model call may
	// stream before it is cancelled; zero means unlimited. See ErrReasoningLimit.
	ReasoningLimit uint64
	// ReplyCheck runs before a text-only response is sent as the agent's reply.
	// A non-empty notice is added to history instead and the model responds
	// again. It holds at most ReplyChecks replies per exchange (default one),
	// so a reply after that is sent.
	ReplyCheck  func(ctx context.Context, self message.ActorID) string
	ReplyChecks int
	// Concurrent names tools whose calls may run together: a response whose
	// calls all name one of them runs them at once, as a web researcher's
	// several page reads. Results still enter history in the order issued.
	Concurrent []string
}

// ErrReasoningLimit marks a model call cancelled for streaming more reasoning
// than Spec.ReasoningLimit allows. Nothing entered history, so the agent
// retries once with a notice asking for the action directly; a second
// consecutive overrun ends the agent.
var ErrReasoningLimit = errors.New("reasoning limit exceeded")

const maxReasoningRetries = 1

// Clone snapshots the prompt and tool list. Provider and tool implementations
// remain shared collaborators and must support concurrent use.
func (s Spec) Clone() Spec {
	s.Prompt = s.Prompt.Clone()
	s.Tools = append([]tool.Tool(nil), s.Tools...)
	s.Concurrent = append([]string(nil), s.Concurrent...)
	return s
}

// Config supplies the collaborators owned by the conversation controller.
type Config struct {
	AdmitInbox InboxAdmission
	Reporter   Reporter // Required for recoverable hosts; nil is an explicitly unrecorded low-level agent.
	ID         message.ActorID
	ReplyTo    message.ActorID
	Spec       Spec
	Inbox      *inbox.Inbox[message.Message]
	Outbox     message.Sender
	// WakeContext attaches harness-owned state once per exchange; see WakeContext.
	WakeContext WakeContext
	// Intake decides which queued messages the agent takes; see Intake.
	Intake Intake
	// Sequence orders the steps that race other agents; see Sequence.
	Sequence Sequence
}

// Sequence orders an agent's steps whose results depend on what other agents
// have done by then: when each tool call starts, since a read such as a
// message's receipt or an agent's state reports the moment it runs, and when
// each inbox message is consumed, which such reads observe. A replay holds
// each at its recorded place so every read sees what it saw when recorded.
// Nil runs freely.
type Sequence interface {
	// ToolStart may block before the call is recorded as started; started is
	// called once it has been.
	ToolStart(ctx context.Context, invocation string) (started func())
	// Consumed reports a message taken into history and acknowledged.
	Consumed(id message.MessageID)
	// Appended reports a message added to the agent's history at position,
	// which reads of the agent's transcript observe.
	Appended(position uint64)
}

// BatchSchedule is implemented by a Sequence that knows how a batch ran: a
// replay runs concurrent tools together only where the recording did, since
// a recording made before calls could run together interleaved each call's
// start with the previous call's result.
type BatchSchedule interface {
	Together(invocations []string) bool
}

// Intake names the queued messages an agent takes when it reads its inbox,
// given the history position the first of them will occupy. Without an
// intake, or when it returns ok false, the agent takes what has arrived: at the
// start of an exchange the oldest message, mid-exchange everything but
// assignments. Which messages have arrived when a turn boundary passes is a
// race between goroutines. A replay names what the recorded agent took there,
// so each message lands where it landed then: the agent waits for every named
// message, takes them in the named order, and leaves the rest queued. starting
// is true when the agent begins an exchange and false when it takes messages
// into one already running, so a message that began an exchange when recorded
// is never pulled into the one before it.
type Intake func(position uint64, starting bool) (ids []message.MessageID, ok bool)

type Agent struct {
	emission       sync.Mutex
	reporting      reportState
	stopRequested  atomic.Bool
	quiescing      atomic.Bool
	nextOutput     uint64
	nextInvocation uint64
	nextWake       uint64
	config         Config
	tools          map[string]tool.Tool
	controls       map[string]tool.ControlKind
	concurrent     map[string]bool
	definitions    []provider.ToolDefinition
	thread         thread
	usage          usageTracker
	started        atomic.Bool
	control        lifecycle
	repeated       repeatedCall
	bookkeeping    map[string]map[string]bool // Tool name to argument names ignored by repeatKey.
}

// repeatedCall counts consecutive tool calls that make the same request and
// get the same outcome, across batches; see repeatKey. A model that re-issues
// one rejected call verbatim, or re-sends an edit that changes nothing, can
// otherwise spend an entire session budget on it.
type repeatedCall struct {
	key   string
	count int
}

func New(config Config) (*Agent, error) {
	if config.Spec.Provider == nil || config.Inbox == nil || config.Outbox == nil {
		return nil, errors.New("agent requires a provider, inbox, and outbox")
	}
	config.Spec = config.Spec.Clone()
	a := &Agent{config: config, tools: make(map[string]tool.Tool), controls: make(map[string]tool.ControlKind), concurrent: make(map[string]bool), bookkeeping: make(map[string]map[string]bool), control: lifecycle{state: Idle, revision: 1, changed: make(chan struct{})}}
	for _, name := range config.Spec.Concurrent {
		a.concurrent[name] = true
	}
	for _, t := range config.Spec.Tools {
		if t == nil {
			return nil, errors.New("nil tool")
		}
		if err := tool.ValidateTool(t); err != nil {
			return nil, fmt.Errorf("invalid tool: %w", err)
		}
		definition := t.Definition()
		if definition.Name == "" {
			return nil, errors.New("tool has no name")
		}
		if _, exists := a.tools[definition.Name]; exists {
			return nil, fmt.Errorf("duplicate tool: %s", definition.Name)
		}
		if control, ok := t.(tool.ControlTool); ok {
			kind := control.Control()
			if kind != tool.YieldToInbox && kind != tool.FinishOnSuccess {
				return nil, fmt.Errorf("invalid tool control: %s", kind)
			}
			a.controls[definition.Name] = kind
		}
		a.tools[definition.Name] = t
		if b, ok := t.(interface{ BookkeepingParameters() []string }); ok {
			ignored := make(map[string]bool)
			for _, name := range b.BookkeepingParameters() {
				ignored[name] = true
			}
			a.bookkeeping[definition.Name] = ignored
		}
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

// maxMalformedCalls bounds regeneration after the provider rejects a tool call
// whose arguments never became complete JSON. The call was not dispatched and
// nothing entered history, so asking again is safe; a persistent failure still
// ends the agent.
const maxMalformedCalls = 2

// repeatedCallHint is the consecutive repeat at which the tool result starts
// carrying a notice; maxRepeatedCalls ends the agent.
const (
	repeatedCallHint = 3
	maxRepeatedCalls = 12
)

// repeatKey identifies a call by what the model asked for and what it got
// back. Bookkeeping arguments the tool declared are dropped, so a retry that
// changes only a copied revision is the same request. The outcome is part of
// the key: the same request with a different error, or a different result, is
// progress and restarts the count. Results are compared by their text after
// dropping top-level revision fields, so a successful edit that changes
// nothing but the revision counter still counts as a repeat.
func (a *Agent) repeatKey(call provider.ToolCall, result tool.Result, err error) string {
	var b strings.Builder
	b.WriteString(call.Name)
	b.WriteByte(0)
	b.WriteString(normalizeArguments(call.Arguments, a.bookkeeping[call.Name]))
	b.WriteByte(0)
	if err != nil {
		b.WriteString("error:" + err.Error())
		return b.String()
	}
	b.WriteString("ok:" + normalizeJSON([]byte(result.Content.Text()), revisionFields))
	return b.String()
}

// revisionFields are the receipt counters harness tools return, plus the
// fresh evidence_ref on each execution receipt; they change on every
// successful call whether or not anything else did.
var revisionFields = map[string]bool{"revision": true, "work_revision": true, "state_revision": true, "evidence_ref": true}

// normalizeArguments ignores bookkeeping only in the canonical input object.
func normalizeArguments(raw []byte, ignored map[string]bool) string {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope["input"] == nil {
		return string(raw)
	}
	envelope["input"] = json.RawMessage(normalizeJSON(envelope["input"], ignored))
	out, err := json.Marshal(envelope)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// normalizeJSON re-encodes a JSON object with the ignored top-level keys
// removed and the remaining keys sorted; anything else is returned as is.
func normalizeJSON(raw []byte, ignored map[string]bool) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return string(raw)
	}
	for name := range ignored {
		delete(object, name)
	}
	out, err := json.Marshal(object) // encoding/json sorts map keys.
	if err != nil {
		return string(raw)
	}
	return string(out)
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
	defer a.finishInterrupt()
	defer func() {
		// A quiesce is the host shutting the conversation down, not a failure
		// and not an abort, so the exit carries no error for consumers to
		// filter. Only work actually interrupted reports cancellation.
		if errors.Is(err, errQuiesced) {
			err = nil
		}
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
	if a.config.Sequence != nil {
		a.config.Sequence.Appended(1)
	}
	for {
		run, cancel, err := a.beginExchange(ctx)
		if err != nil {
			return err
		}
		err = a.exchange(run, &last)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failure := a.reportError(); failure != nil {
			return failure
		}
		if a.interruptPending() {
			if err := a.settleInterrupt(); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

func (a *Agent) exchange(ctx context.Context, last *message.MessageID) error {
	if err := a.waitInbox(ctx); err != nil {
		return err
	}
	incoming, err := a.receive(ctx)
	if err != nil {
		return err
	}
	if incoming.Kind != message.Notification && incoming.Kind != message.Observation {
		*last = incoming.ID
	}
	if err := a.consume(incoming); err != nil {
		return err
	}
	if incoming.Kind == message.Observation {
		return nil
	}
	inputs := []message.Message{incoming}
	admitted := false
	malformed := 0
	overrun := 0
	replyChecks := 0
	for {
		if err := a.checkpoint(ctx); err != nil {
			return err
		}
		// An assignment is a unit of work with its own reply, so one never joins
		// an exchange already running: it stays queued and starts the next one.
		// Consumed mid-exchange, a second assignment to a busy worker was
		// answered by a reply about the first and nothing woke the worker again.
		taken, err := a.take(ctx)
		if err != nil {
			return err
		}
		for _, incoming := range taken {
			if !admitted {
				inputs = append(inputs, incoming)
			}
			if incoming.Kind != message.Notification && incoming.Kind != message.Observation {
				*last = incoming.ID
			}
			if err := a.consume(incoming); err != nil {
				return err
			}
		}
		if err := a.checkpoint(ctx); err != nil {
			return err
		}
		if !admitted {
			wake, err := a.admit(ctx, inputs)
			if err != nil {
				return err
			}
			if !wake {
				break
			}
			admitted = true
			if err := a.appendWakeContext(ctx, inputs); err != nil {
				return err
			}
		}
		request, revision := a.request()
		response, output, err := a.generate(ctx, request, revision)
		var rejected *provider.ToolArgumentsError
		if errors.As(err, &rejected) && ctx.Err() == nil && malformed < maxMalformedCalls {
			malformed++
			notice := fmt.Sprintf("Your previous %s call was discarded and nothing ran: %v. Emit the call again with every parameter closed, one tool call per block.", rejected.Name, err)
			// The notice is a synthetic user message about a finished output.
			// Linking it to that output breaks projection replay, which only
			// accepts assistant messages on an output that is still active.
			if _, err := a.appendHistory(provider.Message{Role: "user", Content: content.Text(notice)}, nil); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, ErrReasoningLimit) && ctx.Err() == nil && overrun < maxReasoningRetries {
			overrun++
			notice := fmt.Sprintf("Your previous response was cut off after %d KB of reasoning without a tool call or reply, and nothing ran. Act now: emit the next tool call or the final reply directly, without further deliberation.", a.config.Spec.ReasoningLimit>>10)
			if _, err := a.appendHistory(provider.Message{Role: "user", Content: content.Text(notice)}, nil); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		malformed = 0
		overrun = 0
		if err := a.checkpoint(ctx); err != nil {
			return err
		}
		if len(response.ToolCalls) == 0 {
			if check := a.config.Spec.ReplyCheck; check != nil && replyChecks < max(1, a.config.Spec.ReplyChecks) {
				replyChecks++
				if notice := check(ctx, a.config.ID); notice != "" {
					// Like the notices above, a synthetic user message not tied to the output.
					if _, err := a.appendHistory(provider.Message{Role: "user", Content: content.Text(notice)}, nil); err != nil {
						return err
					}
					continue
				}
			}
			_, err := a.config.Outbox.Send(ctx, message.Draft{
				To: a.config.ReplyTo, Kind: message.Reply, ReplyTo: *last, Content: response.Content, Output: &output,
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
		}
		var toolRevision uint64
		controlBatch := false
		for _, call := range response.ToolCalls {
			if a.controls[call.Name] == tool.YieldToInbox {
				controlBatch = true
			}
		}
		mixedControl := controlBatch && len(response.ToolCalls) != 1
		yielded := false
		finishedBy := ""
		// A batch whose calls are all concurrent tools runs them at once;
		// the results are settled below in the order the model issued them.
		together := len(response.ToolCalls) > 1 && !controlBatch
		for _, call := range response.ToolCalls {
			together = together && a.concurrent[call.Name]
		}
		if together {
			if schedule, ok := a.config.Sequence.(BatchSchedule); ok {
				ids := make([]string, len(response.ToolCalls))
				for i := range ids {
					ids[i] = fmt.Sprintf("%s/tool-%d", a.config.ID, a.nextInvocation+uint64(i)+1)
				}
				together = schedule.Together(ids)
			}
		}
		var ran []callOutcome
		if together {
			if err := a.checkpoint(ctx); err != nil {
				return err
			}
			ran = a.invokeTogether(ctx, response.ToolCalls)
		}
		for i, call := range response.ToolCalls {
			var result tool.Result
			var err error
			if together {
				result, err = ran[i].result, ran[i].err
			} else {
				if err := a.checkpoint(ctx); err != nil {
					return err
				}
				var rejected error
				if mixedControl {
					rejected = errors.New("control tool must be the sole call; no calls in this batch executed")
				}
				result, err = a.invokeCall(ctx, call, rejected)
			}
			if err == nil && !mixedControl && a.controls[call.Name] == tool.YieldToInbox {
				yielded = true
			}
			if err == nil && !mixedControl && a.controls[call.Name] == tool.FinishOnSuccess && finishedBy == "" {
				finishedBy = call.ID
			}
			if ctx.Err() != nil && err == nil && len(result.Content) == 0 {
				err = ctx.Err()
			}
			if err != nil && result.Execution == nil {
				result.Content = append(result.Content, tool.Text("Tool error: "+err.Error()).Content...)
			}
			if ctx.Err() != nil {
				_, err = a.appendHistory(provider.Message{
					Role: "tool", Content: result.Content.Clone(), ToolCallID: call.ID,
				}, nil)
				if err != nil {
					return err
				}
				return ctx.Err()
			}
			if key := a.repeatKey(call, result, err); key == a.repeated.key {
				a.repeated.count++
			} else {
				a.repeated = repeatedCall{key: key, count: 1}
			}
			if a.repeated.count >= repeatedCallHint {
				result.Content = append(result.Content, tool.Text(fmt.Sprintf("Notice: this is consecutive call %d of %s with the same request and the same result. Repeating it will not change the outcome; change the arguments or the approach instead. After %d such calls this agent stops.", a.repeated.count, call.Name, maxRepeatedCalls)).Content...)
			}

			toolRevision, err = a.appendHistory(provider.Message{
				Role: "tool", Content: result.Content.Clone(), ToolCallID: call.ID,
			}, nil)
			if err != nil {
				return err
			}
			if a.repeated.count >= maxRepeatedCalls {
				return fmt.Errorf("tool %s called %d times in a row with the same request and the same result; stopping", call.Name, a.repeated.count)
			}
		}
		{
			calls := make([]string, len(response.ToolCalls))
			for i, call := range response.ToolCalls {
				calls[i] = call.ID
			}
			if err := a.report(ToolBatch{Calls: calls, ContextRevision: toolRevision}); err != nil {
				return err
			}
		}
		if yielded || finishedBy != "" {
			callID := finishedBy
			if yielded {
				callID = response.ToolCalls[0].ID
			}
			if err := a.report(Yielded{Output: output, CallID: callID, SettledRevision: toolRevision}); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// receive takes the message that starts an exchange.
func (a *Agent) receive(ctx context.Context) (message.Message, error) {
	if a.config.Intake != nil {
		if ids, ok := a.config.Intake(a.thread.length()+1, true); ok && len(ids) > 0 {
			taken, err := a.takeNamed(ctx, ids[:1])
			if err != nil {
				return message.Message{}, err
			}
			return taken[0], nil
		}
	}
	return a.config.Inbox.Receive(ctx)
}

// take takes the messages that join a running exchange.
func (a *Agent) take(ctx context.Context) ([]message.Message, error) {
	if a.config.Intake != nil {
		if ids, ok := a.config.Intake(a.thread.length()+1, false); ok {
			return a.takeNamed(ctx, ids)
		}
	}
	return a.config.Inbox.Take(func(m message.Message) bool { return m.Work == nil }), nil
}

func (a *Agent) takeNamed(ctx context.Context, ids []message.MessageID) ([]message.Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	want := map[message.MessageID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	err := a.config.Inbox.Await(ctx, func(queued []message.Message) bool {
		found := 0
		for _, m := range queued {
			if want[m.ID] {
				found++
			}
		}
		return found == len(want)
	})
	if err != nil {
		return nil, err
	}
	taken := a.config.Inbox.Take(func(m message.Message) bool { return want[m.ID] })
	order := map[message.MessageID]int{}
	for i, id := range ids {
		order[id] = i
	}
	slices.SortStableFunc(taken, func(x, y message.Message) int { return order[x.ID] - order[y.ID] })
	return taken, nil
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
	if a.config.Sequence != nil {
		a.config.Sequence.Consumed(incoming.ID)
	}
	return nil
}

func (a *Agent) request() (provider.Request, uint64) {
	messages, revision := a.thread.requestMessages()
	return provider.Request{Agent: a.config.ID, Messages: messages, Tools: a.Definitions()}, revision
}

type callOutcome struct {
	result tool.Result
	err    error
}

// startedCall is a call that has its invocation and has reported its start.
type startedCall struct {
	call       provider.ToolCall
	invocation string
	started    time.Time
	err        error
}

// invokeTogether starts calls in order, so their invocations and start
// records follow the order issued as a replay expects, then runs them at once.
func (a *Agent) invokeTogether(ctx context.Context, calls []provider.ToolCall) []callOutcome {
	started := make([]startedCall, len(calls))
	for i, call := range calls {
		started[i] = a.startCall(ctx, call)
	}
	out := make([]callOutcome, len(calls))
	var wg sync.WaitGroup
	for i := range started {
		if started[i].err != nil {
			out[i].err = started[i].err
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i].result, out[i].err = a.runCall(ctx, started[i], nil)
		}(i)
	}
	wg.Wait()
	return out
}

func (a *Agent) invokeCall(ctx context.Context, call provider.ToolCall, rejected error) (tool.Result, error) {
	s := a.startCall(ctx, call)
	if s.err != nil {
		return tool.Result{}, s.err
	}
	return a.runCall(ctx, s, rejected)
}

// startCall assigns the call's invocation and reports its start.
func (a *Agent) startCall(ctx context.Context, call provider.ToolCall) startedCall {
	started := time.Now()
	a.nextInvocation++
	invocation := fmt.Sprintf("%s/tool-%d", a.config.ID, a.nextInvocation)
	begun := func() {}
	if a.config.Sequence != nil {
		begun = a.config.Sequence.ToolStart(ctx, invocation)
	}
	err := a.reportTool(ToolActivity{InvocationID: invocation, Call: call, StartedAt: started})
	begun()
	return startedCall{call: call, invocation: invocation, started: started, err: err}
}

// runCall executes a started call and reports its finish.
func (a *Agent) runCall(ctx context.Context, s startedCall, rejected error) (result tool.Result, err error) {
	call, invocation, started := s.call, s.invocation, s.started
	defer func() {
		observedErr := err
		if observedErr == nil {
			observedErr = ctx.Err()
		}
		publishErr := a.reportTool(ToolActivity{InvocationID: invocation, Diagnostic: tool.DiagnosticFrom(observedErr), Call: call, StartedAt: started, FinishedAt: time.Now(), Result: result, Err: observedErr})
		err = errors.Join(err, publishErr)
		if publishErr != nil {
			result = tool.Result{}
		}
	}()
	if rejected != nil {
		return tool.Result{}, rejected
	}
	t, ok := a.tools[call.Name]
	if !ok {
		return tool.Result{}, fmt.Errorf("unknown tool: %s", call.Name)
	}
	if err := tool.ValidateArguments(t, call.Arguments); err != nil {
		return tool.Result{}, err
	}
	return t.Call(ctx, tool.Call{
		InvocationID: invocation, Arguments: append(json.RawMessage(nil), call.Arguments...),
		Actor:  a.config.ID,
		Sender: a.config.Outbox,
	})
}
