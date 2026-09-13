package harness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/roster"
)

// awaitAgentState drains the session event stream until the agent settles in want.
func awaitAgentState(t *testing.T, s *harness.Session, id message.ActorID, want agent.State) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		e, err := s.NextEvent(ctx)
		if err != nil {
			t.Fatal("never observed", want, "for", id, err)
		}
		if c, ok := e.(conversation.AgentStateChanged); ok && c.Agent == id && c.State == want {
			return
		}
	}
}

// Pause, resume and stop are the host's control surface over a running agent.
// Each returns the agent's post-transition snapshot so a caller can act on the
// state without a second inspection round trip.
func TestSessionAgentControlTransitionsThroughPauseResumeStop(t *testing.T) {
	s := newLifecycleSession(t, context.Background(), idle{})
	child := createWorker(t, s, roster.Implementor)

	paused, err := s.PauseAgent(child)
	if err != nil {
		t.Fatal(err)
	}
	if paused.ID != child || paused.State != agent.PauseRequested {
		t.Fatal("pause did not request a halt", paused)
	}
	awaitAgentState(t, s, child, agent.Paused)

	resumed, err := s.ResumeAgent(child)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != agent.Running {
		t.Fatal("resume did not restart the agent", resumed)
	}
	if resumed.StateRevision <= paused.StateRevision {
		t.Fatal("resume did not advance the state revision", resumed.StateRevision, paused.StateRevision)
	}

	stopped, err := s.StopAgent(child)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != agent.StopRequested && !stopped.State.Terminal() {
		t.Fatal("stop left the agent runnable", stopped)
	}
	// Stop is terminal: the agent never accepts control again.
	if _, err := s.PauseAgent(child); err == nil {
		t.Fatal("paused an agent that was already stopping")
	}
	if _, err := s.ResumeAgent(child); err == nil {
		t.Fatal("resumed an agent that was already stopping")
	}
}

// Control over an agent the session never created is reported as a lookup
// failure rather than silently succeeding.
func TestSessionAgentControlRejectsUnknownAgent(t *testing.T) {
	s := newLifecycleSession(t, context.Background(), idle{})
	const missing message.ActorID = "no-such-agent"
	for name, call := range map[string]func() (conversation.AgentInfo, error){
		"pause":  func() (conversation.AgentInfo, error) { return s.PauseAgent(missing) },
		"resume": func() (conversation.AgentInfo, error) { return s.ResumeAgent(missing) },
		"stop":   func() (conversation.AgentInfo, error) { return s.StopAgent(missing) },
	} {
		if _, err := call(); !errors.Is(err, conversation.ErrAgentNotFound) {
			t.Fatal(name, "accepted an unknown agent", err)
		}
	}
}

// Control is admission-gated, so a closed session refuses it instead of
// reaching a controller whose agents have already been torn down.
func TestSessionAgentControlRefusedAfterClose(t *testing.T) {
	ctx := context.Background()
	cfg := harness.DefaultConfig()
	cfg.LocalTools, cfg.Web = false, nil
	s, err := harness.New(ctx, cfg, harness.Dependencies{Provider: idle{}})
	if err != nil {
		t.Fatal(err)
	}
	child := createWorker(t, s, roster.Implementor)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() (conversation.AgentInfo, error){
		"pause":  func() (conversation.AgentInfo, error) { return s.PauseAgent(child) },
		"resume": func() (conversation.AgentInfo, error) { return s.ResumeAgent(child) },
		"stop":   func() (conversation.AgentInfo, error) { return s.StopAgent(child) },
	} {
		if _, err := call(); !errors.Is(err, harness.ErrClosed) {
			t.Fatal(name, "ran on a closed session", err)
		}
	}
}

// Receipt resolves a delivery record projected from the event log, and reports
// absence rather than a zero receipt for an identifier that was never sent.
func TestSessionReceiptResolvesDeliveryAndReportsAbsence(t *testing.T) {
	s := newLifecycleSession(t, context.Background(), idle{})
	sent, err := s.Send(s.Root(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Receipt(sent.MessageID)
	if !ok {
		t.Fatal("sent message had no projected receipt", sent.MessageID)
	}
	if got.MessageID != sent.MessageID || got.Recipient != s.Root() {
		t.Fatal("receipt does not describe the sent message", got)
	}
	if _, ok := s.Receipt("never-sent"); ok {
		t.Fatal("resolved a receipt for a message that was never sent")
	}
}
