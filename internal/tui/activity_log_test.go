package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// Distinct responses let this test detect accidental row/identity reuse, rather
// than mistaking automatic folding or ordinary viewport scrolling for data loss.
type responseScript struct{ calls int }

var successiveResponses = []string{
	"ALPHA original progress.",
	"BRAVO next progress.",
	"CHARLIE latest progress.",
	"DELTA final reply.",
	"ECHO second final reply.",
}

func (p *responseScript) Submit(_ context.Context, _ provider.Request, observer provider.Observer) (provider.Response, error) {
	p.calls++
	if p.calls > len(successiveResponses) {
		return provider.Response{}, fmt.Errorf("unexpected call %d", p.calls)
	}
	text := successiveResponses[p.calls-1]
	if err := observer.OnDelta(provider.Delta{Text: text}); err != nil {
		return provider.Response{}, err
	}
	response := provider.Response{Content: text}
	if p.calls <= 3 {
		response.ToolCalls = []provider.ToolCall{{ID: fmt.Sprintf("read-%d", p.calls), Name: "read_file", Arguments: json.RawMessage(`{"path":"fixture.txt"}`)}}
	}
	return response, nil
}

func TestSuccessiveResponsesRemainVisibleAndRecorded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := harness.DefaultConfig()
	cfg.Dir = dir
	cfg.Web = nil
	cfg.Telemetry.ContextTokens = false
	cfg.Events.JSONLPath = filepath.Join(dir, "session.jsonl")
	session, err := harness.New(ctx, cfg, harness.Dependencies{Provider: &responseScript{}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Dispose(context.Background())
	observed, detach := observeSession(session)
	defer detach()
	m := newModel(ctx, cancel, observed, Options{})
	// All unfolded output fits, ruling out normal viewport scrolling.
	m.Update(tea.WindowSizeMsg{Width: 124, Height: 60})
	if _, err := session.Send(session.Root(), "Run three read-only steps, then reply."); err != nil {
		t.Fatal(err)
	}
	observedThirdCall := false
	replies := 0
	for replies < 2 {
		msg := m.listen()().(received)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		before := ansi.Strip(m.View())
		m.Update(msg)
		after := ansi.Strip(m.View())
		if e, ok := msg.event.(conversation.AgentEvent); ok {
			if start, ok := e.Event.(agent.OutputStarted); ok && start.Output.Call == 3 {
				original := m.outputEntry(identity.OutputID{Agent: session.Root(), Call: 1})
				observedThirdCall = true
				for _, text := range successiveResponses[:2] {
					if !strings.Contains(before, text) || !strings.Contains(after, text) {
						t.Fatalf("next response hid earlier text %q", text)
					}
				}
				if original == nil || original.body != successiveResponses[0] {
					t.Fatal("a later call replaced an earlier entry")
				}
			}
		}
		if e, ok := msg.event.(conversation.MessageEvent); ok && e.Message.Kind == message.Reply && e.Message.To == message.User {
			replies++
			if replies == 1 {
				if _, err := session.Send(session.Root(), "Reply once more."); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if !observedThirdCall {
		t.Fatal("fixture did not exercise the next response starting")
	}
	for i, want := range successiveResponses {
		id := identity.OutputID{Agent: session.Root(), Call: uint64(i + 1)}
		row := m.outputEntry(id)
		if row == nil || row.body != want {
			t.Fatalf("live entry %v: %+v", id, row)
		}
		output, err := session.InspectOutput(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := session.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Through: output.Output.Through, MaxBytes: 4096})
		if err != nil || stored.Text != want {
			t.Fatalf("log output %v: %q, %v", id, stored.Text, err)
		}
	}
	agentView, err := session.InspectAgent(session.Root(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	var assistantTexts []string
	for _, entry := range agentView.Transcript.Entries {
		if entry.Message.Role == "assistant" {
			assistantTexts = append(assistantTexts, entry.Message.Content.Text())
		}
	}
	if strings.Join(assistantTexts, "\n") != strings.Join(successiveResponses, "\n") {
		t.Fatalf("model history changed: %q", assistantTexts)
	}
	visible := ansi.Strip(m.View())
	for _, want := range successiveResponses {
		if !strings.Contains(visible, want) {
			t.Fatalf("final reply replaced: %q", want)
		}
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	archive, err := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(context.Background())
	view, err := archive.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range successiveResponses {
		id := identity.OutputID{Agent: session.Root(), Call: uint64(i + 1)}
		stored, err := view.ReadOutputText(ctx, inspection.OutputTextQuery{Output: id, MaxBytes: 4096})
		if err != nil || stored.Text != want {
			t.Fatalf("reopened archive output %v: %q, %v", id, stored.Text, err)
		}
	}
	t.Log("all five distinct responses retained in TUI entries, model history, live log, and reopened JSONL; all progress and final replies remained visible")
}
