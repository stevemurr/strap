package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
)

// Hold the HTTP response open until each prefix is visible in the TUI. This
// exercises provider streaming, log publication, subscription and UI rendering
// together, and fails if any layer waits for the completed response.
func TestTUIRendersHTTPStreamBeforeCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reasonVisible := make(chan struct{})
	next := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Stream {
			t.Errorf("expected streaming request: stream=%v, err=%v", request.Stream, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"Early reasoning"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-reasonVisible:
		case <-ctx.Done():
			return
		}

		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Early text\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-next:
		case <-ctx.Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" arrives\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-ctx.Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := harness.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.Web, cfg.LocalTools = nil, false
	cfg.Telemetry.ContextTokens = false
	cfg.Model.BaseURL = server.URL
	session, err := harness.New(ctx, cfg, harness.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Dispose(context.Background())
	observed, detach := observeSession(session)
	defer detach()
	m := newModel(ctx, cancel, observed, Options{})
	m.input.SetValue("unfinished draft")
	if _, err := session.Send(session.Root(), "respond"); err != nil {
		t.Fatal(err)
	}
	reasoningVisible, firstVisible, secondVisible := false, false, false
	for {
		msg := m.listen()().(received)
		if msg.err != nil {
			t.Fatalf("stream stopped before reply: %v", msg.err)
		}
		m.Update(msg)
		view := ansi.Strip(m.View())
		if !reasoningVisible && strings.Contains(view, "Early reasoning") {
			reasoningVisible = true
			close(reasonVisible)
		}
		if !firstVisible && strings.Contains(view, "Early text") {
			firstVisible = true
			close(next)
		}
		if !secondVisible && strings.Contains(view, "Early text arrives") {
			secondVisible = true
			close(finish)
		}
		if m.input.Value() != "unfinished draft" {
			t.Fatal("streaming overwrote draft input")
		}
		if e, ok := msg.event.(conversation.MessageEvent); ok && e.Message.Kind == message.Reply && e.Message.To == message.User {
			if !reasoningVisible || !firstVisible || !secondVisible || strings.Count(view, "Early text arrives") != 1 {
				t.Fatalf("expected one incrementally rendered reply:\n%s", view)
			}
			if strings.Contains(view, "Early reasoning") {
				t.Fatal("reasoning did not auto-collapse")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyF3})
			if !strings.Contains(ansi.Strip(m.View()), "Early reasoning") {
				t.Fatal("cannot expand reasoning")
			}
			// Clearing display rows must not destroy reasoning inspection.
			enter(m, "/clear")
			m.openTranscript(session.Root())
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
			if cmd == nil {
				t.Fatal("reasoning inspection did not load")
			}
			m.Update(cmd())
			if !strings.Contains(ansi.Strip(m.View()), "Early reasoning") || m.transcript.err != "" {
				t.Fatal(m.View())
			}
			if strings.Contains(ansi.Strip(m.View()), "Early text arrives") {
				t.Fatal("reasoning inspection mixed answer history")
			}
			return
		}
	}
}
