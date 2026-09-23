package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
)

type reasoningTestSession struct {
	*transcriptSession
	before uint64
	err    error
}

func (s *reasoningTestSession) readReasoning(_ context.Context, actor identity.ActorID, before uint64) ([]reasoningOutput, bool, error) {
	s.before = before
	if s.err != nil {
		return nil, false, s.err
	}
	if before == 2 {
		return []reasoningOutput{{Output: projection.OutputView{ID: identity.OutputID{Agent: actor, Call: 1}, Status: agent.OutputComplete}, Text: "older reasoning"}}, false, nil
	}
	return []reasoningOutput{{Output: projection.OutputView{ID: identity.OutputID{Agent: actor, Call: 2}, Status: agent.OutputFailed, Error: &eventlog.Problem{Code: "generation_failed", Message: "length limit"}}, Text: "failed partial reasoning"}}, true, nil
}

func TestReasoningInspectionIncludesFailedCallsPagesAndIgnoresStaleReads(t *testing.T) {
	m, s := transcriptSetup(t)
	source := &reasoningTestSession{transcriptSession: s}
	m.session = source
	m.openTranscript("root")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd == nil {
		t.Fatal("no inspection command")
	}
	result := cmd()
	m.Update(result)
	if !strings.Contains(m.View(), "failed partial reasoning") || !strings.Contains(m.View(), "length limit") || !strings.Contains(m.View(), "completed responses retained in model history") {
		t.Fatal(m.View())
	}
	if strings.Contains(m.View(), "root message") {
		t.Fatal("mixed model history into reasoning inspection")
	}
	m.transcript.viewport.GotoTop()
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if cmd == nil {
		t.Fatal("no earlier output page")
	}
	m.Update(cmd())
	if source.before != 2 || len(m.transcript.reasoningOutputs) != 2 || m.transcript.reasoningEarlier {
		t.Fatal(source.before, m.transcript)
	}
	source.err = errors.New("read failed")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m.Update(cmd())
	if !strings.Contains(m.View(), "read failed") || strings.Contains(m.View(), "Loading…") {
		t.Fatal(m.View())
	}
	m.openTranscript("agent-7")
	m.Update(result)
	if strings.Contains(m.View(), "failed partial reasoning") || m.transcript.inspection.ID != "agent-7" {
		t.Fatal("stale read replaced current view")
	}
	if len(s.sent) != 0 {
		t.Fatal("inspection sent a model message")
	}
}
