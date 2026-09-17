package tui

import (
	"strings"
	"testing"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
)

func TestStopInterruptsSessionAndTerminateRemainsExplicit(t *testing.T) {
	m, s := setup(t)
	m.input.SetValue("/stop")
	_, cmd := m.submit()
	if cmd == nil || !m.interrupting {
		t.Fatal("stop did not start asynchronous interruption")
	}
	if !strings.Contains(m.status(), "Stopping") {
		t.Fatal(m.status())
	}
	m.Update(cmd())
	if s.managed != "interrupt" || m.interrupting || m.rootStopped {
		t.Fatal("stop terminated root or failed to settle")
	}
	m.observe(conversation.AgentStateChanged{Agent: "root", State: agent.Interrupted})
	if !strings.Contains(m.status(), "new instruction") {
		t.Fatal(m.status())
	}
	m.input.SetValue("/stop root")
	m.submit()
	if s.managed != "interrupt" {
		t.Fatal("old stop syntax terminated agent")
	}
	m.input.SetValue("/terminate root")
	m.submit()
	if s.managed != "stop:root" {
		t.Fatal("explicit termination was lost")
	}
}
