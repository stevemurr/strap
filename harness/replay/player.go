package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
)

// Divergence is the replay departing from the recording.
type Divergence struct {
	Agent  identity.ActorID `json:"agent,omitempty"`
	Call   int              `json:"call,omitempty"` // The agent's model call, from 1.
	Kind   string           `json:"kind"`           // request, tools, extra_call, environment, stall.
	Detail string           `json:"detail"`
}

func (d Divergence) String() string {
	if d.Agent == "" {
		return fmt.Sprintf("%s: %s", d.Kind, d.Detail)
	}
	if d.Call == 0 {
		return fmt.Sprintf("%s %s: %s", d.Agent, d.Kind, d.Detail)
	}
	return fmt.Sprintf("%s call %d %s: %s", d.Agent, d.Call, d.Kind, d.Detail)
}

// gate is one recorded point the replay must pass in order: a model output
// returning, an environment call returning, or a user message being sent.
type gate struct {
	key string
	seq uint64
	at  time.Time // When the recording reached it.
}

// target is the model call a probe takes instead of answering.
type target struct {
	agent    identity.ActorID
	call     int
	requests chan provider.Request
}

// player serves a recording to a live session.
type player struct {
	rec   *Recording
	stall time.Duration

	mu          sync.Mutex
	changed     chan struct{}
	gates       []gate
	next        int             // Index of the first gate not yet passed.
	passed      map[string]bool // Gates passed out of order after a stall skipped them.
	waiting     map[string]int  // Gates something is blocked at.
	calls       map[identity.ActorID]int
	byAgent     map[identity.ActorID][]int // Indexes into rec.Outputs.
	diverged    map[identity.ActorID]bool  // Agents whose first request divergence is reported.
	divergences []Divergence
	served      int
	progress    time.Time
	known       map[string]bool
	target      *target
	clock       *virtualClock
	dir         string // The replay's workspace, standing in for the recorded one.
	sent        map[identity.ActorID]int
	testers     map[identity.ActorID][]conversation.TesterEvent // Tester runs not yet served.
	senders     map[identity.ActorID]*sync.Mutex
}

func newPlayer(rec *Recording, stall time.Duration) *player {
	p := &player{rec: rec, stall: stall, changed: make(chan struct{}), passed: map[string]bool{}, waiting: map[string]int{}, calls: map[identity.ActorID]int{},
		byAgent: map[identity.ActorID][]int{}, diverged: map[identity.ActorID]bool{}, progress: time.Now(),
		sent: map[identity.ActorID]int{}, senders: map[identity.ActorID]*sync.Mutex{}}
	count := map[identity.ActorID]int{}
	for _, m := range rec.Sends {
		count[m.From]++
		p.gates = append(p.gates, gate{key: sendGate(m.From, count[m.From]), seq: m.Sequence})
	}
	p.testers = map[identity.ActorID][]conversation.TesterEvent{}
	for _, e := range rec.TesterRuns {
		p.testers[e.Agent] = append(p.testers[e.Agent], e)
	}
	for i, o := range rec.Outputs {
		p.byAgent[o.Agent] = append(p.byAgent[o.Agent], i)
		p.gates = append(p.gates, gate{key: outputGate(o.ID), seq: o.Sequence})
	}
	for id, seq := range rec.EnvironmentAt {
		p.gates = append(p.gates, gate{key: "environment " + id, seq: seq})
	}
	for i, u := range rec.Users {
		p.gates = append(p.gates, gate{key: userGate(i), seq: u.Sequence})
	}
	for invocation, seq := range rec.ToolStarts {
		p.gates = append(p.gates, gate{key: toolGate(invocation), seq: seq})
	}
	for _, a := range rec.Appends {
		p.gates = append(p.gates, gate{key: appendGate(a.Agent, a.Position), seq: a.Sequence})
	}
	for _, c := range rec.Consumptions {
		p.gates = append(p.gates, gate{key: consumedGate(c.Agent, c.ID), seq: c.Sequence})
	}
	sort.Slice(p.gates, func(i, j int) bool { return p.gates[i].seq < p.gates[j].seq })
	p.clock = newVirtualClock(rec.Times[1]) // The session's first record.
	for i := range p.gates {
		p.gates[i].at = rec.Times[p.gates[i].seq]
	}
	p.advanceLocked()
	p.known = map[string]bool{}
	for _, g := range p.gates {
		p.known[g.key] = true
	}
	return p
}

// Together reports whether the recording ran a batch's calls together: its
// second call started before the first call's result entered history.
func (p *player) Together(invocations []string) bool {
	if len(invocations) < 2 {
		return false
	}
	first, ok1 := p.rec.ToolStarts[invocations[0]]
	second, ok2 := p.rec.ToolStarts[invocations[1]]
	if !ok1 || !ok2 {
		return false
	}
	agent, _, _ := strings.Cut(invocations[0], "/")
	for _, a := range p.rec.Appends {
		if string(a.Agent) == agent && a.Sequence > first {
			if a.Sequence < second {
				return false
			}
		}
	}
	return true
}

func outputGate(id identity.OutputID) string { return fmt.Sprintf("output %s/%d", id.Agent, id.Call) }
func userGate(i int) string                  { return fmt.Sprintf("user message %d", i+1) }
func toolGate(invocation string) string      { return "tool start " + invocation }
func appendGate(agent identity.ActorID, position uint64) string {
	return fmt.Sprintf("%s history %d", agent, position)
}
func consumedGate(agent identity.ActorID, id message.MessageID) string {
	return fmt.Sprintf("%s consumed %s", agent, id)
}
func sendGate(from identity.ActorID, n int) string {
	return fmt.Sprintf("message %d from %s", n, from)
}

func (p *player) divergeLocked(d Divergence) { p.divergences = append(p.divergences, d) }

func (p *player) notifyLocked() {
	p.progress = time.Now()
	close(p.changed)
	p.changed = make(chan struct{})
}

// advanceLocked moves the virtual clock to the recorded time of the last gate
// passed: everything recorded up to it has happened. Events the harness
// stamps from here on are no later than when they were recorded, so a notice
// they schedule never falls due after its own recorded send.
func (p *player) advanceLocked() {
	for p.next < len(p.gates) && p.passed[p.gates[p.next].key] {
		p.next++
	}
	if p.next > 0 && !p.gates[p.next-1].at.IsZero() {
		p.clock.advance(p.gates[p.next-1].at)
	}
}

// idle is how long nothing may move before the replay lets the clock reach
// the next recorded point: whatever it is waiting for may be a notice that
// only time sends.
const idle = 50 * time.Millisecond

// reachNextLocked lets the clock reach the next recorded point's time.
func (p *player) reachNextLocked() {
	if p.next < len(p.gates) && !p.gates[p.next].at.IsZero() {
		p.clock.advance(p.gates[p.next].at)
	}
}

// await blocks until every gate recorded before key has passed. When nothing
// moves for the stall period, the gates holding the replay up, up to the next
// one something waits at, are reported and skipped: the replay has already
// departed from the recording there.
func (p *player) await(ctx context.Context, key string) error {
	p.mu.Lock()
	p.waiting[key]++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.waiting[key]--; p.waiting[key] == 0 {
			delete(p.waiting, key)
		}
		p.mu.Unlock()
	}()
	for {
		p.mu.Lock()
		for p.next < len(p.gates) && p.passed[p.gates[p.next].key] {
			p.next++
		}
		if p.next >= len(p.gates) || p.gates[p.next].key == key {
			p.mu.Unlock()
			return nil
		}
		wait, since := p.changed, time.Since(p.progress)
		if since >= idle {
			p.reachNextLocked()
		}
		if since >= p.stall {
			var skipped []string
			for i := p.next; i < len(p.gates) && p.waiting[p.gates[i].key] == 0; i++ {
				if !p.passed[p.gates[i].key] {
					skipped = append(skipped, p.gates[i].key)
					p.passed[p.gates[i].key] = true
				}
			}
			p.divergeLocked(Divergence{Kind: "stall", Detail: fmt.Sprintf("nothing reached %s for %s while %s waited; skipped %d recorded points: %s", skipped[0], p.stall, key, len(skipped), clip(strings.Join(skipped, ", "), 300))})
			p.notifyLocked()
			p.mu.Unlock()
			continue
		}
		p.mu.Unlock()
		pause := p.stall - since
		if since < idle {
			pause = idle - since
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		case <-time.After(pause):
		}
	}
}

func (p *player) pass(key string) {
	p.mu.Lock()
	p.passed[key] = true
	p.advanceLocked()
	p.notifyLocked()
	p.mu.Unlock()
}

// done reports whether every gate has passed.
func (p *player) done() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.next < len(p.gates) && p.passed[p.gates[p.next].key] {
		p.next++
	}
	return p.next >= len(p.gates)
}

// Submit answers an agent's model call with the output it recorded for that
// call, once every earlier recorded point has passed.
func (p *player) Submit(ctx context.Context, req provider.Request, obs provider.Observer) (provider.Response, error) {
	agentID := identity.ActorID(req.Agent)
	p.mu.Lock()
	n := p.calls[agentID]
	p.calls[agentID]++
	indexes := p.byAgent[agentID]
	if n >= len(indexes) {
		p.divergeLocked(Divergence{Agent: agentID, Call: n + 1, Kind: "extra_call", Detail: fmt.Sprintf("the recording has %d model calls for this agent", len(indexes))})
		p.notifyLocked()
		p.mu.Unlock()
		<-ctx.Done() // Never answered: the recording has nothing to say.
		return provider.Response{}, ctx.Err()
	}
	o := p.rec.Outputs[indexes[n]]
	if t := p.target; t != nil && t.agent == agentID && t.call == n+1 {
		p.mu.Unlock()
		select {
		case t.requests <- req:
		default:
		}
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	}
	if !p.diverged[agentID] {
		if d, ok := p.compare(agentID, n+1, o, req); ok {
			p.diverged[agentID] = true
			p.divergeLocked(d)
		}
	}
	p.mu.Unlock()
	key := outputGate(o.ID)
	if err := p.await(ctx, key); err != nil {
		return provider.Response{}, err
	}
	defer p.pass(key)
	p.mu.Lock()
	p.served++
	p.mu.Unlock()
	if obs != nil {
		for _, d := range o.Deltas {
			if err := obs.OnDelta(d); err != nil {
				return provider.Response{}, err
			}
		}
	}
	switch {
	case o.Status == agent.OutputCanceled:
		// The recorded call ended because its agent was interrupted or
		// stopped; the replayed one ends the same way, when that happens.
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	case o.Rejected != nil:
		rejected := *o.Rejected
		return provider.Response{}, &rejected
	case o.Response == nil:
		if o.Err == "" {
			o.Err = fmt.Sprintf("recorded output ended %s", o.Status)
		}
		return provider.Response{}, errors.New(o.Err)
	}
	return provider.Response{Reasoning: o.Response.Reasoning, Content: o.Response.Content.Text(), ToolCalls: provider.CopyCalls(o.Response.ToolCalls)}, nil
}

// timestamps are the one thing a faithful replay still changes: the ledger
// stamps records with the wall clock.
var timestamps = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)

func canonical(v any) string {
	raw, _ := json.Marshal(v)
	return timestamps.ReplaceAllString(string(raw), "<time>")
}

// compare checks a request against the recorded one: the agent's history
// through the output's context revision, and the tools its role recorded.
func (p *player) compare(agentID identity.ActorID, call int, o Output, req provider.Request) (Divergence, bool) {
	want := p.rec.Histories[agentID]
	if uint64(len(want)) < o.Context {
		return Divergence{Agent: agentID, Call: call, Kind: "request", Detail: fmt.Sprintf("recording holds %d history messages, output needs %d", len(want), o.Context)}, true
	}
	want = want[:o.Context]
	for i := 0; i < max(len(want), len(req.Messages)); i++ {
		switch {
		case i >= len(want):
			return Divergence{Agent: agentID, Call: call, Kind: "request", Detail: fmt.Sprintf("message %d is extra: %s", i+1, clip(canonical(req.Messages[i]), 400))}, true
		case i >= len(req.Messages):
			return Divergence{Agent: agentID, Call: call, Kind: "request", Detail: fmt.Sprintf("message %d is missing: %s", i+1, clip(canonical(want[i]), 400))}, true
		}
		if w, g := canonical(want[i]), canonical(req.Messages[i]); w != g {
			return Divergence{Agent: agentID, Call: call, Kind: "request", Detail: fmt.Sprintf("message %d (%s) differs at byte %d:\n  recorded: %s\n  replayed: %s", i+1, want[i].Role, firstDiff(w, g), around(w, firstDiff(w, g)), around(g, firstDiff(w, g)))}, true
		}
	}
	role, ok := p.rec.RoleConfig(p.rec.Role(agentID))
	if !ok {
		return Divergence{}, false
	}
	if w, g := canonical(role.Tools), canonical(req.Tools); w != g {
		return Divergence{Agent: agentID, Call: call, Kind: "tools", Detail: fmt.Sprintf("tool definitions differ at byte %d:\n  recorded: %s\n  replayed: %s", firstDiff(w, g), around(w, firstDiff(w, g)), around(g, firstDiff(w, g)))}, true
	}
	return Divergence{}, false
}

func firstDiff(a, b string) int {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func around(s string, at int) string {
	start := max(0, at-80)
	end := min(len(s), at+160)
	out := s[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(s) {
		out += "…"
	}
	return out
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// environment serves an environment tool's recorded results in place of it.
type environment struct {
	tool.Tool
	p *player
}

func (p *player) environment(t tool.Tool) tool.Tool { return &environment{t, p} }

// Definition describes the tool as the recorded session did. Environment
// tools describe the machine they run on, such as the shell's directory and
// program; a replay runs elsewhere, but the model must see what it saw.
func (t *environment) Definition() provider.ToolDefinition {
	d := t.Tool.Definition()
	d.Description = t.p.localize(d.Description)
	return d
}

// RecordedChanges reports the workspace changes the recorded call made, so
// the replay's own record carries them although nothing ran.
func (t *environment) RecordedChanges(invocation string) ([]string, bool, bool, bool) {
	e, ok := t.p.rec.Environment[invocation]
	return slices.Clone(e.Changed), e.Unscanned, e.Scanned, ok
}

func (t *environment) InputContract() tool.Contract {
	if typed, ok := t.Tool.(interface{ InputContract() tool.Contract }); ok {
		return typed.InputContract()
	}
	return tool.Contract{}
}
func (t *environment) Validate() error { return tool.ValidateTool(t.Tool) }
func (t *environment) BookkeepingParameters() []string {
	if b, ok := t.Tool.(interface{ BookkeepingParameters() []string }); ok {
		return b.BookkeepingParameters()
	}
	return nil
}

func (t *environment) Call(ctx context.Context, c tool.Call) (tool.Result, error) {
	name := t.Definition().Name
	if e, ok := t.p.rec.Environment[c.InvocationID]; ok {
		t.check(c, name, e.Name, e.Arguments)
		key := "environment " + c.InvocationID
		if err := t.p.await(ctx, key); err != nil {
			return tool.Result{}, err
		}
		defer t.p.pass(key)
		result := tool.Result{Content: e.Content.Clone(), Captured: e.Captured.Clone()}
		if e.Err != "" {
			return result, errors.New(e.Err)
		}
		return result, nil
	}
	// Traces made before environment records hold the call's error, or the
	// result as the model saw it, which may include harness wrapping.
	a, ok := t.p.rec.Activities[c.InvocationID]
	if !ok {
		t.p.mu.Lock()
		t.p.divergeLocked(Divergence{Agent: c.Actor, Kind: "environment", Detail: fmt.Sprintf("%s %s was never called in the recording", name, c.InvocationID)})
		t.p.mu.Unlock()
		return tool.Result{}, fmt.Errorf("replay: %s has no recorded result", c.InvocationID)
	}
	t.check(c, name, a.Name, a.Args)
	if a.Err != "" {
		return tool.Result{}, errors.New(a.Err)
	}
	for _, m := range t.p.rec.Histories[c.Actor] {
		if m.Role == "tool" && m.ToolCallID == a.CallID {
			return tool.Result{Content: m.Content.Clone()}, nil
		}
	}
	return tool.Result{}, fmt.Errorf("replay: %s has no recorded result", c.InvocationID)
}

func (t *environment) check(c tool.Call, name, recordedName string, recordedArgs json.RawMessage) {
	if name == recordedName && sameArguments(c.Arguments, recordedArgs) {
		return
	}
	t.p.mu.Lock()
	t.p.divergeLocked(Divergence{Agent: c.Actor, Kind: "environment", Detail: fmt.Sprintf("%s ran %s %s; the recording ran %s %s", c.InvocationID, name, clip(string(c.Arguments), 200), recordedName, clip(string(recordedArgs), 200))})
	t.p.mu.Unlock()
}

// shellFacts reads the directory and program a shell description names.
var shellFacts = regexp.MustCompile(`Run a synchronous shell command in (.+?) using (\S+)\. `)

// localize replaces the replay machine's facts with the recorded ones.
func (p *player) localize(description string) string {
	if p.dir != "" && p.dir != p.rec.Config.Dir {
		// Tools may name the directory as given or with symlinks resolved;
		// the longer form goes first so no prefix of it is left behind.
		forms := []string{p.dir}
		if real, err := filepath.EvalSymlinks(p.dir); err == nil && real != p.dir {
			forms = append(forms, real)
		}
		sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
		for _, f := range forms {
			description = strings.ReplaceAll(description, f, p.rec.Config.Dir)
		}
	}
	live := shellFacts.FindStringSubmatch(description)
	if live == nil {
		return description
	}
	for _, role := range p.rec.Config.Roles() {
		for _, d := range role.Tools {
			if recorded := shellFacts.FindStringSubmatch(d.Description); d.Name == "shell" && recorded != nil {
				return strings.Replace(description, live[0], recorded[0], 1)
			}
		}
	}
	return description
}

// sameArguments compares tool arguments as JSON values. Tool records made
// before environment records hold them as a JSON string.
func sameArguments(a, b json.RawMessage) bool {
	value := func(raw json.RawMessage) string {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return string(raw)
		}
		if s, ok := v.(string); ok && json.Unmarshal([]byte(s), &v) != nil {
			return s
		}
		return canonical(v)
	}
	return value(a) == value(b)
}

// sendOrder admits each sender's messages at their recorded place among all
// the others, so the controller numbers them as it did when recorded. One
// sender's messages go one at a time: the host and the agent itself both
// send as the agent.
func (p *player) sendOrder(ctx context.Context, from identity.ActorID) func(sent bool) {
	if from == message.User {
		return func(bool) {} // The replay sends user messages at their gates.
	}
	p.mu.Lock()
	lock := p.senders[from]
	if lock == nil {
		lock = &sync.Mutex{}
		p.senders[from] = lock
	}
	p.mu.Unlock()
	lock.Lock()
	p.mu.Lock()
	key := sendGate(from, p.sent[from]+1)
	recorded := p.known[key]
	if !recorded {
		p.divergeLocked(Divergence{Agent: from, Kind: "message", Detail: fmt.Sprintf("sent message %d; the recording has %d from this sender", p.sent[from]+1, p.sent[from])})
	}
	p.mu.Unlock()
	if recorded {
		_ = p.await(ctx, key)
	}
	return func(sent bool) {
		if sent {
			p.mu.Lock()
			p.sent[from]++
			p.mu.Unlock()
			p.pass(key)
		}
		lock.Unlock()
	}
}

// sequence holds each agent's tool starts and consumptions at their recorded
// places, so a read of another agent's progress sees what it saw then.
type sequence struct {
	p     *player
	agent identity.ActorID
}

func (p *player) sequence(agent identity.ActorID) agent.Sequence { return sequence{p, agent} }

func (s sequence) ToolStart(ctx context.Context, invocation string) func() {
	key := toolGate(invocation)
	s.p.mu.Lock()
	recorded := s.p.known[key]
	s.p.mu.Unlock()
	if !recorded {
		return func() {}
	}
	_ = s.p.await(ctx, key)
	return func() { s.p.pass(key) }
}

// Together runs a batch's calls together only where the recording did.
func (s sequence) Together(invocations []string) bool { return s.p.Together(invocations) }

func (s sequence) Appended(position uint64) {
	key := appendGate(s.agent, position)
	s.p.mu.Lock()
	recorded := s.p.known[key]
	s.p.mu.Unlock()
	if recorded {
		s.p.pass(key)
	}
}

func (s sequence) Consumed(id message.MessageID) {
	key := consumedGate(s.agent, id)
	s.p.mu.Lock()
	recorded := s.p.known[key]
	s.p.mu.Unlock()
	if recorded {
		s.p.pass(key)
	}
}

// intake has each agent take the inbox messages its recording took at the
// same history position, whatever order they arrive in, and in the same
// exchange.
func (p *player) intake(actor identity.ActorID) agent.Intake {
	return func(position uint64, starting bool) ([]message.MessageID, bool) {
		return p.rec.Taken(actor, position, starting)
	}
}

// workspace lists what each agent's first wake listed when recorded.
func (p *player) workspace(actor identity.ActorID, _ string) *message.Workspace {
	w, ok := p.rec.Workspaces[actor]
	if !ok {
		return nil
	}
	v := *w
	v.Entries = append([]string(nil), w.Entries...)
	return &v
}

// testerRun serves an agent's next recorded tester run: the tester is the
// harness's own helper, and the failures it returned are all the agent saw.
func (p *player) testerRun(agent identity.ActorID) (conversation.TesterEvent, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	runs := p.testers[agent]
	if len(runs) == 0 {
		return conversation.TesterEvent{}, false
	}
	p.testers[agent] = runs[1:]
	return runs[0], true
}
