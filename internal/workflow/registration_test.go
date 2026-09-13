package workflow

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/roster"
	"github.com/stevemurr/strap/work"
	"sync/atomic"
	"testing"
	"time"
)

type countingIdle struct{ calls atomic.Int32 }

func (p *countingIdle) Submit(context.Context, provider.Request, provider.Observer) (provider.Response, error) {
	p.calls.Add(1)
	return provider.Response{Content: "ready"}, nil
}
func TestCreateAgentIdleRolesAndAssignmentHasNoRuntimeSideEffects(t *testing.T) {
	ctx, s := recoverySession(t)
	p := &countingIdle{}
	s.implementor.Provider = p
	impl, e := s.CreateAgent(ctx, s.Root(), roster.CreateRequest{Role: roster.Implementor})
	if e != nil {
		t.Fatal(e)
	}
	info, e := s.Controller.InspectAgent(impl.AgentID, conversation.InspectOptions{})
	if e != nil || info.State != agent.Idle || p.calls.Load() != 0 || len(s.Store.PendingEvents(0)) != 0 {
		t.Fatal(info, e, p.calls.Load())
	}
	for _, role := range []roster.Role{"", roster.Root, "writer"} {
		if _, e = s.CreateAgent(ctx, s.Root(), roster.CreateRequest{Role: role}); !errors.Is(e, work.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e = s.CreateAgent(ctx, impl.AgentID, roster.CreateRequest{Role: roster.Auditor}); !errors.Is(e, work.ErrForbidden) {
		t.Fatal(e)
	}
	for _, assignee := range []identity.ActorID{"", s.Root(), "missing"} {
		if _, e = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: assignee, Task: "task"}); !errors.Is(e, work.ErrInvalid) {
			t.Fatal(e)
		}
	}
	if len(s.Agents()) != 2 {
		t.Fatal("invalid operations created agents")
	}
	// Registration does not follow automatically from the low-level runtime API.
	raw, e := s.Controller.CreateAgent(s.Root(), agent.Spec{Provider: idleProvider{}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: raw.AgentID, Task: "task"}); !errors.Is(e, work.ErrInvalid) {
		t.Fatal(e)
	}
	// Assignment failure must leave the already-created implementor available.
	if _, e = s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: impl.AgentID, Task: "task", Scope: &work.Scope{PlanID: "missing", StepIDs: []work.StepID{"missing"}}}); !errors.Is(e, work.ErrNotFound) {
		t.Fatal(e)
	}
	info, _ = s.Controller.InspectAgent(impl.AgentID, conversation.InspectOptions{})
	if info.State != agent.Idle || p.calls.Load() != 0 {
		t.Fatal(info)
	}
}
func TestRegistrationPublicationFailureStopsUnregisteredRuntime(t *testing.T) {
	sentinel := errors.New("registration failed")
	publish := func(e conversation.Event) error {
		if _, ok := e.(conversation.AgentRegistered); ok {
			return sentinel
		}
		return nil
	}
	ctx, s := publishedSession(t, publish)
	if _, e := s.CreateAgent(ctx, s.Root(), roster.CreateRequest{Role: roster.Implementor}); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	agents := s.Agents()
	if len(agents) != 2 {
		t.Fatal(agents)
	}
	child := agents[1]
	s.mu.Lock()
	_, ok := s.roles[child.ID]
	s.mu.Unlock()
	if ok {
		t.Fatal("failed registration became eligible")
	}
	info, _ := s.Controller.InspectAgent(child.ID, conversation.InspectOptions{})
	if info.State != agent.StopRequested && !info.State.Terminal() {
		t.Fatal(info)
	}
}
func TestAssignmentWaitsForRegistrationPublication(t *testing.T) {
	entered := make(chan identity.ActorID, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	publish := func(e conversation.Event) error {
		if v, ok := e.(conversation.AgentRegistered); ok {
			entered <- v.Registration.AgentID
			<-release
		}
		return nil
	}
	ctx, s := publishedSession(t, publish)
	creation := make(chan error, 1)
	go func() {
		_, e := s.CreateAgent(ctx, s.Root(), roster.CreateRequest{Role: roster.Implementor})
		creation <- e
	}()
	var id identity.ActorID
	select {
	case id = <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assignment := make(chan error, 1)
	go func() {
		_, e := s.AssignWork(ctx, s.Root(), work.AssignmentRequest{Kind: work.Implementation, Assignee: id, Task: "task"})
		assignment <- e
	}()
	select {
	case e := <-assignment:
		t.Fatal("assignment escaped registration publication", e)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if e := <-creation; e != nil {
		t.Fatal(e)
	}
	if e := <-assignment; e != nil {
		t.Fatal(e)
	}
}

func publishedSession(t *testing.T, publish func(conversation.Event) error) (context.Context, *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	t.Cleanup(cancel)
	c := conversation.New(ctx)
	s := New(ctx, c, agent.Spec{Provider: idleProvider{}}, agent.Spec{Provider: idleProvider{}}, WithPublisher(publish))
	if _, e := c.CreateAgent("user", agent.Spec{Provider: idleProvider{}}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	return ctx, s
}
