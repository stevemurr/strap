package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stevemurr/strap/roster"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/inbox"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

func ptr[T any](v T) *T { return &v }

type cycleProvider struct {
	mu          sync.Mutex
	session     *Session
	plan        work.Plan
	manager     message.ActorID
	implementor message.ActorID
	auditor     message.ActorID
	audited     map[message.ActorID]bool // Auditors already assigned; every audit gets a new one.
	repaired    map[work.AuditID]bool
	assigned    bool
	reviews     map[work.SubmissionID]bool
	calls       int
	failed      bool
	done        chan work.Work
}

func (p *cycleProvider) Submit(ctx context.Context, r provider.Request, observer provider.Observer) (provider.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	invoke := func(name string, args any) (provider.Response, error) {
		raw, e := tool.MarshalInput(args)
		return provider.Response{ToolCalls: []provider.ToolCall{{ID: fmt.Sprintf("call-%d", p.calls), Name: name, Arguments: raw}}}, e
	}
	for _, m := range r.Messages {
		if m.Role == "tool" && strings.HasPrefix(m.Content.Text(), "Tool error:") {
			return provider.Response{}, fmt.Errorf("script tool failed: %s", m.Content.Text())
		}
	}
	if r.Agent == p.manager {
		for _, m := range r.Messages {
			if m.Role == "tool" {
				var reg roster.Registration
				if json.Unmarshal([]byte(m.Content.Text()), &reg) == nil && reg.AgentID != "" {
					if reg.Role == roster.Implementor {
						p.implementor = reg.AgentID
					}
					if reg.Role == roster.Auditor {
						p.auditor = reg.AgentID
					}
				}
			}
		}
		if p.implementor == "" {
			return invoke("create_agent", roster.CreateRequest{Role: roster.Implementor})
		}
		if p.auditor == "" {
			return invoke("create_agent", roster.CreateRequest{Role: roster.Auditor})
		}
		if !p.assigned {
			p.assigned = true
			return invoke("assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(p.implementor), Task: "implement storage", Scope: &work.Scope{PlanID: p.plan.ID, StepIDs: []work.StepID{p.plan.Steps[0].ID, p.plan.Steps[1].ID}}})
		}
		for i := len(r.Messages) - 1; i >= 0; i-- {
			m := r.Messages[i]
			if m.Envelope == nil || m.Envelope.Event == nil {
				continue
			}
			e := m.Envelope.Event
			if e.Kind == work.AuditCompleted && e.Work.State == work.ChangesRequested && !p.repaired[e.AuditID] {
				w, err := p.session.Store.GetWork(p.manager, e.Work.ID)
				if err != nil {
					return provider.Response{}, err
				}
				if w.State == work.ChangesRequested && w.ActiveRepairID == "" {
					if p.repaired == nil {
						p.repaired = map[work.AuditID]bool{}
					}
					p.repaired[e.AuditID] = true
					return invoke("assign_repair", tool.AssignRepairArgs{Assignee: p.implementor, WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, AuditID: e.AuditID})
				}
			}
			if e.Kind == work.AuditCompleted && e.Work.State == work.Accepted {
				select {
				case p.done <- e.Work:
				default:
				}
				return provider.Response{Content: "Audited outcome accepted."}, nil
			}
			if e.Kind == work.ReviewRequested && !p.reviews[e.SubmissionID] {
				w, err := p.session.Store.GetWork(p.manager, e.Work.ID)
				if err != nil {
					return provider.Response{}, err
				}
				if w.State != work.NeedsCheck {
					continue
				}
				if p.audited[p.auditor] {
					return invoke("create_agent", roster.CreateRequest{Role: roster.Auditor})
				}
				if p.audited == nil {
					p.audited = map[message.ActorID]bool{}
				}
				p.audited[p.auditor] = true
				p.reviews[e.SubmissionID] = true
				return invoke("assign_audit", tool.AssignAuditArgs{Assignee: p.auditor, WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, SubmissionID: w.LatestSubmissionID})
			}
		}
		return provider.Response{Content: "Waiting for the work cycle."}, nil
	}
	var snapshot *work.Work
	for _, m := range r.Messages {
		if m.Envelope != nil && m.Envelope.Work != nil {
			snapshot = m.Envelope.Work
		}
	}
	if snapshot == nil {
		return provider.Response{}, fmt.Errorf("missing work envelope")
	}
	w, err := p.session.Store.GetWork(r.Agent, snapshot.ID)
	if err != nil {
		return provider.Response{}, err
	}
	for _, def := range r.Tools {
		if def.Name == "assign_task" || def.Name == "create_agent" {
			return provider.Response{}, fmt.Errorf("delegation leaked to worker")
		}
		if w.Kind == work.AuditWork && def.Name == "submit_work" {
			return provider.Response{}, fmt.Errorf("implementation tool leaked to auditor")
		}
	}
	if w.State != work.Active {
		return provider.Response{Content: "Outcome submitted."}, nil
	}
	target := work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}
	if w.Kind == work.AuditWork {
		req := work.AuditRequest{WorkTarget: target, SubmissionID: w.SubjectSubmissionID, Verdict: work.Pass, Summary: "All requirements verified"}
		if !p.failed {
			p.failed = true
			req.Verdict = work.Fail
			req.Summary = "Missing error handling"
			req.Findings = []work.Finding{{StepIDs: []work.StepID{p.plan.Steps[0].ID}, Description: "write failure ignored", RequiredChange: "propagate failure", Verification: "test failed write"}}
		}
		// Real callers may explicitly send [] for a pass. Exercise that wire
		// shape instead of relying on AuditRequest's omitempty serialization.
		if req.Findings == nil {
			req.Findings = []work.Finding{}
		}
		return invoke("submit_audit", map[string]any{
			"work_id": req.ID, "expected_revision": req.ExpectedRevision,
			"submission_id": req.SubmissionID, "verdict": req.Verdict,
			"summary": req.Summary, "findings": req.Findings,
		})
	}
	plan, err := p.session.Store.GetPlan(r.Agent, w.Scope.PlanID)
	if err != nil {
		return provider.Response{}, err
	}
	var changes []work.StepProgress
	for _, step := range plan.Steps {
		for _, id := range w.Scope.StepIDs {
			if step.ID == id && step.Status != work.ReadyForReview {
				changes = append(changes, work.StepProgress{ID: id, Status: ptr(work.ReadyForReview)})
			}
		}
	}
	if len(changes) > 0 {
		inputs := make([]tool.StepProgressInput, len(changes))
		for i, v := range changes {
			inputs[i] = tool.StepProgressInput{ID: v.ID, Status: v.Status, Note: v.Note}
		}
		return invoke("report_work_progress", tool.ReportWorkProgressInput{WorkID: target.ID, Steps: inputs})
	}
	return invoke("submit_work", tool.SubmitInput{WorkTarget: target, Summary: "Implemented and checked", Evidence: []string{"scripted evidence"}})
}
func TestFullCycleWithoutUIReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &cycleProvider{reviews: map[work.SubmissionID]bool{}, done: make(chan work.Work, 1)}
	c := conversation.New(ctx)
	s := New(ctx, c, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "implementor"}}, agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "auditor"}})
	p.session = s
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if e := s.Close(cleanup); e != nil {
			t.Error(e)
		}
	})
	s.UseManager(agent.Spec{Provider: p, Prompt: prompt.Prompt{Role: "manager"}, Tools: s.CoordinationTools()})
	manager, err := s.CreateManager(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.manager = manager
	// Exercise the public creation contract, rather than constructing the plan
	// directly in the store and bypassing the model-facing boundary.
	for _, operation := range s.CoordinationTools() {
		if operation.Definition().Name != "create_plan" {
			continue
		}
		created, err := operation.Call(ctx, tool.Call{Actor: p.manager, Arguments: json.RawMessage(`{"input":{"title":"Storage","steps":[{"title":"Implement","acceptance_criteria":null},{"title":"Test","acceptance_criteria":null},{"title":"Integrate","acceptance_criteria":null}]}}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(created.Content.Text()), &p.plan); err != nil {
			t.Fatal(err)
		}
	}
	if p.plan.ID == "" {
		t.Fatal("plan tool did not create a plan")
	}
	if _, err = c.Send(p.manager, "Begin"); err != nil {
		t.Fatal(err)
	}
	// Deliberately never call Session.NextEvent while the workflow runs.
	select {
	case w := <-p.done:
		if w.State != work.Accepted {
			t.Fatal(w)
		}
	case <-ctx.Done():
		t.Fatal("work cycle did not finish", ctx.Err())
	}
	plan, err := s.Store.GetPlan(p.manager, p.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0].Status != work.Completed || plan.Steps[1].Status != work.Completed || plan.Steps[2].Status != work.Pending {
		t.Fatal(plan)
	}
	p.mu.Lock()
	reviews := len(p.reviews)
	p.mu.Unlock()
	if reviews != 2 {
		t.Fatal("expected audit and reaudit", reviews)
	}
	if len(c.Agents()) != 4 { // Manager, one implementor, two auditors.
		t.Fatal("repair should reuse implementor", c.Agents())
	}
	// Existing implementors cannot be selected as auditors by the host adapter.
	var impl message.ActorID
	for _, r := range s.graph.Agents() {
		if r.Role == roster.Implementor {
			impl = r.AgentID
		}
	}
	if err := s.eligible(coord(s), impl, work.AuditWork); err == nil {
		t.Fatal("role check failed")
	}
}

type idleProvider struct{}

func (idleProvider) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	return provider.Response{Content: "waiting"}, nil
}
func TestOldBindingFailureAndPendingAssignment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := conversation.New(ctx)
	spec := agent.Spec{Provider: idleProvider{}}
	root, e := c.CreateAgent(message.User, spec)
	if e != nil {
		t.Fatal(e)
	}
	old, e := c.CreateAgent(root.AgentID, spec)
	if e != nil {
		t.Fatal(e)
	}
	next, e := c.CreateAgent(root.AgentID, spec)
	if e != nil {
		t.Fatal(e)
	}
	// Exercise dispatcher with stale work queued before it starts.
	store := work.New()
	w, e := store.AssignWork(root.AgentID, work.AssignRequest{Assignee: old.AgentID, Task: "work"})
	if e != nil {
		t.Fatal(e)
	}
	oldBinding := binding{w.ID, w.AssignedAtRevision, w.Assignee, w.Owner}
	// The work is cancelled and assigned again to another agent before the
	// dispatcher runs: only the new assignment may be delivered.
	if _, e = store.Cancel(root.AgentID, work.CancelRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Reason: "replaced"}); e != nil {
		t.Fatal(e)
	}
	w, e = store.AssignWork(root.AgentID, work.AssignRequest{Assignee: next.AgentID, Task: "work"})
	if e != nil {
		t.Fatal(e)
	}
	sctx, stop := context.WithCancel(ctx)
	s := &Session{Controller: c, Store: store, ctx: sctx, cancel: stop, events: inbox.New[conversation.Event](), done: make(chan struct{})}
	if s.current(oldBinding) {
		t.Fatal("old binding still current")
	}
	go s.run()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = s.Close(cleanup)
	}()
	for {
		event, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m, ok := event.(conversation.MessageEvent); ok && m.Message.Work != nil {
			if m.Message.To != next.AgentID || m.Message.Work.ID != w.ID || m.Message.Work.AssignedAtRevision != w.AssignedAtRevision {
				t.Fatal("stale assignment delivered")
			}
			break
		}
	}
}

// recoverySession boots a session the way the harness does: the session's one
// manager, which the user talks to and which coordinates all work. coord
// returns it.
func recoverySession(t *testing.T) (context.Context, *Session) {
	t.Helper()
	return recoverySessionWith(t, agent.Spec{Provider: idleProvider{}})
}

func recoverySessionWith(t *testing.T, manager agent.Spec) (context.Context, *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)
	c := conversation.New(ctx)
	s := New(ctx, c, agent.Spec{Provider: idleProvider{}}, agent.Spec{Provider: idleProvider{}})
	manager.Tools = append(manager.Tools, s.CoordinationTools()...)
	s.UseManager(manager)
	if _, err := s.CreateManager(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if e := s.Close(cleanup); e != nil {
			t.Error(e)
		}
	})
	return ctx, s
}

// coord returns the session's manager, the only agent that coordinates work.
func coord(s *Session) message.ActorID { return s.graph.Find(roster.Manager) }

func invokeManager(t *testing.T, s *Session, name string, args any) tool.Result {
	t.Helper()
	raw, e := tool.MarshalInput(args)
	if e != nil {
		t.Fatal(e)
	}
	for _, op := range s.CoordinationTools() {
		if op.Definition().Name == name {
			v, e := op.Call(context.Background(), tool.Call{Actor: coord(s), Arguments: raw})
			if e != nil {
				t.Fatal(e)
			}
			return v
		}
	}
	t.Fatal("missing tool", name)
	return tool.Result{}
}
func assigned(t *testing.T, s *Session) work.Work {
	t.Helper()
	result := invokeManager(t, s, "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(createWorker(t, s, roster.Implementor)), Task: "task"})
	var w work.Work
	if e := json.Unmarshal([]byte(result.Content.Text()), &w); e != nil {
		t.Fatal(e)
	}
	return w
}
func nextEvent(t *testing.T, ctx context.Context, s *Session, predicate func(conversation.Event) bool) conversation.Event {
	t.Helper()
	for {
		event, e := s.NextEvent(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if predicate(event) {
			return event
		}
	}
}
func TestAuditCancellationWakesManagerAndSubmissionNeedsNoLiveImplementor(t *testing.T) {
	ctx, s := recoverySession(t)
	w := assigned(t, s)
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.Work != nil && m.Message.Work.ID == w.ID
	})
	sub, e := s.Store.SubmitWork(w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "done"})
	if e != nil {
		t.Fatal(e)
	}
	if s.current(binding{w.ID, w.AssignedAtRevision, w.Assignee, w.Owner}) {
		t.Fatal("submitted work still requires active execution")
	}
	original, _ := s.Store.GetWork(coord(s), w.ID)
	result := invokeManager(t, s, "assign_audit", tool.AssignAuditArgs{Assignee: createWorker(t, s, roster.Auditor), WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: original.Revision}, SubmissionID: sub.ID})
	var audit work.Work
	_ = json.Unmarshal([]byte(result.Content.Text()), &audit)
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.Work != nil && m.Message.Work.ID == audit.ID
	})
	invokeManager(t, s, "cancel_work", work.CancelRequest{WorkTarget: work.WorkTarget{ID: audit.ID, ExpectedRevision: audit.Revision}, Reason: "replace audit"})
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.To == audit.Assignee && m.Message.Event != nil && m.Message.Event.Kind == work.WorkCancelled
	})
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.To == coord(s) && m.Message.Event != nil && m.Message.Event.Kind == work.ReviewRequested && m.Message.Event.Actor == coord(s)
	})
}
func TestUndeliveredOwnerNotificationRemainsPending(t *testing.T) {
	ctx, s := recoverySession(t)
	w := assigned(t, s)
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.Work != nil && m.Message.Work.ID == w.ID
	})
	if _, e := s.Controller.PauseAgent(coord(s)); e != nil {
		t.Fatal(e)
	}
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		state, ok := e.(conversation.AgentStateChanged)
		return ok && state.Agent == coord(s) && state.State == agent.Paused
	})
	if _, e := s.Store.SubmitWork(w.Assignee, work.SubmitRequest{WorkTarget: work.WorkTarget{ID: w.ID, ExpectedRevision: w.Revision}, Summary: "done"}); e != nil {
		t.Fatal(e)
	}
	var id work.EventID
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		if ok && m.Message.To == coord(s) && m.Message.Event != nil && m.Message.Event.Kind == work.ReviewRequested {
			id = m.Message.Event.ID
			return true
		}
		return false
	})
	if _, e := s.Controller.StopAgent(coord(s)); e != nil {
		t.Fatal(e)
	}
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.Kind == message.Failure && strings.Contains(m.Message.Content, "remains pending")
	})
	found := false
	for _, event := range s.Store.PendingEvents(0) {
		if event.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("unconsumed review notification lost")
	}
}
func TestPausedImplementorExitHasOneRecoveryNotification(t *testing.T) {
	ctx, s := recoverySession(t)
	reg, e := s.CreateAgent(ctx, coord(s), roster.CreateRequest{Role: roster.Implementor})
	id := reg.AgentID
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Controller.PauseAgent(id); e != nil {
		t.Fatal(e)
	}
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		state, ok := e.(conversation.AgentStateChanged)
		return ok && state.Agent == id && state.State == agent.Paused
	})
	value := invokeManager(t, s, "assign_task", tool.AssignTaskArgs{Kind: work.Implementation, Assignee: ptr(id), Task: "paused work"})
	var w work.Work
	_ = json.Unmarshal([]byte(value.Content.Text()), &w)
	nextEvent(t, ctx, s, func(e conversation.Event) bool {
		m, ok := e.(conversation.MessageEvent)
		return ok && m.Message.Work != nil && m.Message.Work.ID == w.ID
	})
	if _, e = s.Controller.StopAgent(id); e != nil {
		t.Fatal(e)
	}
	count := 0
	for {
		event, e := s.NextEvent(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if m, ok := event.(conversation.MessageEvent); ok && m.Message.Kind == message.Notification && strings.Contains(m.Message.Content, "delivery/execution needs attention") {
			count++
		}
		if m, ok := event.(conversation.MessageEvent); ok && m.Message.From == coord(s) && m.Message.Kind == message.Reply && count > 0 {
			break
		}
	}
	if count != 1 {
		t.Fatal("expected one recovery notification", count)
	}
}

func createWorker(t *testing.T, s *Session, role roster.Role) message.ActorID {
	t.Helper()
	v := invokeManager(t, s, "create_agent", roster.CreateRequest{Role: role})
	var r roster.Registration
	if e := json.Unmarshal([]byte(v.Content.Text()), &r); e != nil {
		t.Fatal(e)
	}
	return r.AgentID
}
