package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/eventlog"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/harness/eventcodec"
	"github.com/stevemurr/strap/harness/inspection"
	"github.com/stevemurr/strap/harness/projection"
	"github.com/stevemurr/strap/identity"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
)

func TestReasoningHistoryReachesNextHTTPRequestAndArchive(t *testing.T) {
	for _, backend := range []string{"vllm", "chatcompletions"} {
		t.Run(backend, func(t *testing.T) { testReasoningHistoryRecovery(t, backend) })
	}
}

func testReasoningHistoryRecovery(t *testing.T, backend string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requests := make(chan string, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		requests <- string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning":"PRIVATE_REASON_\ud83c\udf0e"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"reasoning":"_TAIL"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"index":0,"delta":{"content":" done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := testConfig(t, false)
	cfg.Telemetry.ContextTokens = false
	cfg.Model.BaseURL = server.URL
	cfg.Model.Backend = backend
	cfg.Events.JSONLPath = filepath.Join(t.TempDir(), "reasoning.jsonl")
	s, err := harness.New(ctx, cfg, harness.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose(context.Background())
	sub, err := s.Subscribe(ctx, harness.SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, err = s.Send(s.Root(), "first"); err != nil {
		t.Fatal(err)
	}
	view := projection.New(identity.SessionID(s.ID()))
	var cursor eventlog.Cursor
	for {
		e, err := sub.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = view.Apply(e); err != nil {
			t.Fatal(err)
		}
		if e.Kind == "output_delta" {
			cursor = e.Cursor()
			break
		}
	}
	id := identity.OutputID{Agent: s.Root(), Call: 1}
	active, err := s.InspectOutput(ctx, id)
	if err != nil || active.Output.Status != agent.OutputActive || active.Output.ReasoningBytes != uint64(len("PRIVATE_REASON_🌎")) || active.Output.TextBytes != 0 {
		t.Fatal(active, err)
	}
	page, err := s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Channel: provider.ChannelReasoning, Through: cursor, MaxBytes: 14})
	if err != nil || page.Text != "PRIVATE_REASON" || page.End {
		t.Fatal(page, err)
	}
	page, err = s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Channel: provider.ChannelReasoning, Through: cursor, Offset: page.Next, MaxBytes: 8})
	if err != nil || page.Text != "_🌎" || !page.End {
		t.Fatal(page, err)
	}
	sub.Close()
	close(release)
	sub, err = s.Subscribe(ctx, harness.SubscribeOptions{After: cursor})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	waitReply := func() {
		t.Helper()
		for {
			e, err := sub.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = view.Apply(e); err != nil {
				t.Fatal(err)
			}
			e, err = s.ResolveRecord(ctx, e)
			if err != nil {
				t.Fatal(err)
			}
			v, err := eventcodec.DecodeEvent(e)
			if err != nil {
				t.Fatal(err)
			}
			if m, ok := v.(conversation.MessageEvent); ok && m.Message.Kind == message.Reply && m.Message.To == message.User {
				if m.Message.Content != "answer done" {
					t.Fatal(m)
				}
				return
			}
		}
	}
	waitReply()
	if _, err = s.Send(s.Root(), "second"); err != nil {
		t.Fatal(err)
	}
	waitReply()
	<-requests
	next := <-requests
	var body struct {
		Messages []struct {
			Role, Content, Reasoning string
			ReasoningContent         string `json:"reasoning_content"`
		}
	}
	if err := json.Unmarshal([]byte(next), &body); err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, m := range body.Messages {
		if m.Role == "assistant" {
			assistants++
			if m.Content != "answer done" || m.Reasoning != "PRIVATE_REASON_🌎_TAIL" || m.ReasoningContent != m.Reasoning {
				t.Fatalf("reasoning/answer history lost or mixed: %+v", m)
			}
		} else if m.Reasoning != "" || m.ReasoningContent != "" {
			t.Fatalf("reasoning on non-assistant message: %+v", m)
		}
	}
	if assistants != 1 {
		t.Fatalf("expected one previous assistant response, got %d", assistants)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// A fixed prefix stays readable even after later content and reasoning arrive.
	page, err = s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Channel: provider.ChannelReasoning, Through: cursor, MaxBytes: 100})
	if err != nil || page.Text != "PRIVATE_REASON_🌎" {
		t.Fatal(page, err)
	}
	head, _ := s.Events(ctx, eventlog.Query{Limit: 1000})
	for _, channel := range []provider.OutputChannel{provider.ChannelContent, provider.ChannelReasoning} {
		page, err = s.ReadOutputText(ctx, harness.OutputTextQuery{Output: id, Channel: channel, Through: eventlog.Cursor{Session: s.ID(), Sequence: head.Latest}, MaxBytes: 100})
		want := "answer done"
		if channel == provider.ChannelReasoning {
			want = "PRIVATE_REASON_🌎_TAIL"
		}
		if err != nil || page.Text != want || page.Channel != channel {
			t.Fatal(page, err)
		}
	}
	if err = s.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	archive, err := eventlog.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close(context.Background())
	replay := projection.New(identity.SessionID(s.ID()))
	records, err := archive.Read(ctx, eventlog.Query{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range records.Events {
		if err = replay.Apply(e); err != nil {
			t.Fatal(err)
		}
	}
	output, err := replay.Output(id)
	if err != nil || output.ReasoningBytes != uint64(len("PRIVATE_REASON_🌎_TAIL")) || output.TextBytes != 11 || output.Status != agent.OutputComplete {
		t.Fatal(output, err)
	}
	reader, err := inspection.OpenJSONL(ctx, cfg.Events.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	recovered, err := reader.At(ctx, eventlog.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := recovered.InspectAgentContext(ctx, s.Root(), conversation.InspectOptions{Transcript: &agent.TranscriptQuery{Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	assistants = 0
	for _, entry := range info.Transcript.Entries {
		if m := entry.Message; m.Role == "assistant" {
			assistants++
			if m.Reasoning != "PRIVATE_REASON_🌎_TAIL" || m.Content.Text() != "answer done" {
				t.Fatalf("archive lost assistant reasoning: %+v", m)
			}
		}
	}
	if assistants != 2 {
		t.Fatalf("expected both assistant responses in archive, got %d", assistants)
	}
}
