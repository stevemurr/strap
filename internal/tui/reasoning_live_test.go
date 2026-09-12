package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

// This test uses a live model but sends only a synthetic prompt and disables
// local/browser tools. Run with STRAP_LIVE_BASE_URL set to a thinking endpoint.
func TestLiveReasoningTUI(t *testing.T) {
	base := os.Getenv("STRAP_LIVE_BASE_URL")
	if base == "" {
		t.Skip("set STRAP_LIVE_BASE_URL to run live reasoning smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web = nil
	cfg.LocalTools = false
	cfg.Telemetry.ContextTokens = false
	cfg.Model.BaseURL = base
	if model := os.Getenv("STRAP_LIVE_MODEL"); model != "" {
		cfg.Model.Model = model
	}
	limit := 8192
	cfg.Model.Generation.MaxTokens = &limit
	session, err := harness.New(ctx, cfg, harness.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Dispose(context.Background())
	observed, detach := observeSession(session)
	defer detach()
	m := newModel(ctx, cancel, observed, Options{})
	start := time.Now()
	if _, err = session.Send(session.Root(), "For this smoke test, answer what 17 plus 25 equals. Keep reasoning brief, do not call tools, and reply in one sentence."); err != nil {
		t.Fatal(err)
	}
	var id identity.OutputID
	var reasoning strings.Builder
	chunks := 0
	activeObserved := false
	for {
		msg := m.listen()().(received)
		if msg.err != nil {
			t.Fatal(msg.err)
		}
		m.Update(msg)
		if e, ok := msg.event.(conversation.AgentEvent); ok {
			switch d := e.Event.(type) {
			case agent.OutputDelta:
				if d.Channel == provider.ChannelReasoning {
					id = d.Output
					reasoning.WriteString(d.Text)
					chunks++
					if chunks == 1 {
						row := m.outputEntry(id)
						if row == nil || !strings.Contains(ansi.Strip(m.View()), "Reasoning") || !strings.Contains(ansi.Strip(m.View()), strings.TrimSpace(safeText(d.Text))) {
							t.Fatal("first reasoning chunk did not render")
						}
						inspection, err := session.InspectOutput(ctx, id)
						if err != nil {
							t.Fatal(err)
						}
						activeObserved = inspection.Output.Status == agent.OutputActive
						t.Logf("first rendered reasoning at %s; generation active=%v", time.Since(start), activeObserved)
					}
				}
			case agent.OutputFinished:
				if d.Err != nil {
					t.Fatal(d.Err)
				}
			}
		}
		if e, ok := msg.event.(conversation.MessageEvent); ok && e.Message.Kind == message.Reply && e.Message.To == message.User {
			if chunks == 0 || !activeObserved {
				t.Fatal("did not observe live reasoning before completion")
			}
			if !strings.Contains(e.Message.Content, "42") {
				t.Fatal("unexpected arithmetic reply")
			}
			inspection, err := session.InspectOutput(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var stored strings.Builder
			for offset := uint64(0); offset < inspection.Output.ReasoningBytes; {
				page, err := session.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Channel: provider.ChannelReasoning, Through: inspection.Output.Through, Offset: offset, MaxBytes: 64 << 10})
				if err != nil {
					t.Fatal(err)
				}
				stored.WriteString(page.Text)
				offset = page.Next
			}
			if stored.String() != reasoning.String() {
				t.Fatal("recovered reasoning differs from streamed text")
			}
			in, err := session.InspectAgent(session.Root(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Limit: 100}})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range in.Transcript.Entries {
				if strings.Contains(entry.Message.Content.Text(), reasoning.String()) {
					t.Fatal("reasoning entered model history")
				}
			}
			// Inspect the retained reasoning through the TUI's on-demand view too.
			m.openTranscript(session.Root())
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
			if cmd == nil {
				t.Fatal("inspection did not load")
			}
			m.Update(cmd())
			if m.transcript.err != "" || len(m.transcript.reasoningOutputs) == 0 || m.transcript.reasoningOutputs[0].Text != reasoning.String() {
				t.Fatal("TUI inspection did not recover reasoning")
			}
			t.Logf("reply completed at %s; reasoning chunks=%d bytes=%d; retained inspection and history exclusion verified", time.Since(start), chunks, reasoning.Len())
			return
		}
	}
}
