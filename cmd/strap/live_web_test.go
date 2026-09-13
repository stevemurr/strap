package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/harness"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/tool"
)

// STRAP_LIVE_WEB=1 STRAP_LIVE_BASE_URL=http://localhost:8000 go test ./cmd/strap -run TestLiveWebResearch -count=1 -v
func TestLiveWebResearch(t *testing.T) {
	base := os.Getenv("STRAP_LIVE_BASE_URL")
	if os.Getenv("STRAP_LIVE_WEB") != "1" || base == "" {
		t.Skip("set STRAP_LIVE_WEB=1 and STRAP_LIVE_BASE_URL for a real-model research check")
	}
	model := os.Getenv("STRAP_LIVE_MODEL")
	if model == "" {
		model = "qwen3.6"
	}
	p, err := (harness.ModelConfig{Backend: "vllm", BaseURL: base, Model: model, Generation: vllm.Generation{MaxTokens: valuePtr(8192)}}).NewProvider(&http.Client{Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	w, err := tool.NewWeb(tool.WebConfig{WKRenderPath: os.Getenv("STRAP_WKRENDER"), AgentBrowserPath: os.Getenv("STRAP_AGENT_BROWSER"), BrowserExecutablePath: os.Getenv("STRAP_BROWSER_EXECUTABLE")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := conversation.New(ctx)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := c.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	_, err = c.CreateAgent(message.User, agent.Spec{Provider: p, Prompt: harness.DefaultConfig().Root.Prompt, Tools: w.Tools()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(c.Root(), "Search for the official Go context package documentation, open that source, and briefly explain what context.WithCancel does and when its cancel function should be called. Cite the page you read. No code changes are needed.")
	if err != nil {
		t.Fatal(err)
	}
	searches, reads, commentary := 0, 0, 0
	var sources []string
	for {
		e, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch e := e.(type) {
		case conversation.CommentaryEvent:
			commentary++
			t.Log("progress:", e.Content)
		case conversation.ToolEvent:
			if e.Activity.FinishedAt.IsZero() || e.Activity.Err != nil {
				continue
			}
			switch e.Activity.Call.Name {
			case "web_search":
				searches++
			case "open_url":
				var page tool.OpenURLResult
				if err := json.Unmarshal([]byte(e.Activity.Result.Content.Text()), &page); err != nil {
					t.Fatal(err)
				}
				reads++
				sources = append(sources, page.FinalURL)
			}
		case conversation.MessageEvent:
			if e.Message.To != message.User || e.Message.Kind != message.Reply {
				continue
			}
			cited := false
			for _, source := range sources {
				cited = cited || strings.Contains(e.Message.Content, source)
			}
			if searches == 0 || reads == 0 || !cited {
				t.Fatalf("research did not search/read/cite: searches=%d reads=%d reply=%s", searches, reads, e.Message.Content)
			}
			t.Logf("%d searches, %d page reads, %d progress updates; final: %s", searches, reads, commentary, e.Message.Content)
			return
		case conversation.AgentExited:
			t.Fatalf("agent exited before answering: %v", e.Err)
		}
	}
}
