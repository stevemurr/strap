package conversation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/agent"
	"github.com/stevemurr/strap/conversation"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/prompt"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/tool"
	"github.com/stevemurr/strap/work"
)

// Exercise the production controller, agent loop, and HTTP adapter together.
// Checking only provider.Request would miss prompt loss during wire translation.
func TestDelegatedPromptAndAssignmentReachHTTPServer(t *testing.T) {
	rootPrompt := prompt.Prompt{Role: "Coordinate", Instructions: []string{"Root-only operating instructions."}}
	executionPrompt := prompt.Prompt{Role: "Execute", Instructions: []string{"Agent-only operating instructions.", "Complete assigned work."}}
	rootSystem, err := rootPrompt.Render()
	if err != nil {
		t.Fatal(err)
	}
	agentSystem, err := executionPrompt.Render()
	if err != nil {
		t.Fatal(err)
	}
	assignment := work.Work{Task: "Compute a result", Context: "Quoted \"context\"\nsecond line", ExpectedOutput: "A report"}
	args, err := tool.MarshalInput(map[string]string{"task": assignment.Task, "context": assignment.Context, "expected_output": assignment.ExpectedOutput})
	if err != nil {
		t.Fatal(err)
	}
	var rootCalls, agentCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if request.Model != "test-model" || len(request.Messages) < 2 || request.Messages[0].Role != "system" {
			t.Errorf("invalid request: %+v", request)
			http.Error(w, "bad messages", 400)
			return
		}
		hasCreation := false
		for _, operation := range request.Tools {
			if operation.Function.Name == "create_test_agent" {
				hasCreation = true
			}
		}
		if hasCreation != (request.Messages[0].Content == rootSystem) {
			t.Errorf("wrong creation tool scope for %q: %+v", request.Messages[0].Content, request.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Messages[0].Content {
		case rootSystem:
			if rootCalls.Add(1) == 1 {
				response := map[string]any{"choices": []any{map[string]any{
					"finish_reason": "tool_calls",
					"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
						"id": "delegate-1", "type": "function", "function": map[string]any{"name": "create_test_agent", "arguments": string(args)},
					}}},
				}}}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
				return
			}
			for _, m := range request.Messages {
				if m.Role != "user" {
					continue
				}
				var envelope message.Message
				if err := json.Unmarshal([]byte(m.Content), &envelope); err != nil {
					t.Error(err)
					continue
				}
				if envelope.From != message.User && envelope.Kind == message.Reply {
					fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
					return
				}
			}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"waiting"}}]}`)
		case agentSystem:
			agentCalls.Add(1)
			if len(request.Messages) != 2 || request.Messages[1].Role != "user" {
				t.Errorf("unexpected delegated history: %+v", request.Messages)
			}
			var envelope message.Message
			if err := json.Unmarshal([]byte(request.Messages[1].Content), &envelope); err != nil {
				t.Error(err)
			}
			if envelope.From == message.User || envelope.From == "" || envelope.To == envelope.From || envelope.ID == "" || envelope.Kind != message.Instruction || envelope.Content != "" || envelope.Work == nil || *envelope.Work != assignment {
				t.Errorf("assignment lost on wire: %+v", envelope)
			}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"result"}}]}`)
		default:
			t.Errorf("unexpected system prompt at HTTP server: %q", request.Messages[0].Content)
			http.Error(w, "wrong prompt", 400)
		}
	}))
	defer server.Close()
	p, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "test-model", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := conversation.New(ctx)
	executionSpec := agent.Spec{Provider: p, Prompt: executionPrompt, Tools: []tool.Tool{tool.SendMessage(), tool.MessageStatus(c.Receipt)}}
	rootTools := append(append([]tool.Tool(nil), executionSpec.Tools...), creationTool(c, executionSpec))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := c.Close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	_, err = c.CreateAgent(message.User, agent.Spec{Provider: p, Prompt: rootPrompt, Tools: rootTools})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(c.Root(), "delegate"); err != nil {
		t.Fatal(err)
	}
	for {
		e, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := e.(conversation.AgentExited); ok && e.Err != nil {
			t.Fatal(e.Err)
		}
		if e, ok := e.(conversation.MessageEvent); ok && e.Message.To == message.User && e.Message.Content == "done" {
			break
		}
	}
	if agentCalls.Load() != 1 || rootCalls.Load() < 2 {
		t.Fatalf("unexpected HTTP calls: root=%d agent=%d", rootCalls.Load(), agentCalls.Load())
	}
	if len(c.Agents()) != 2 {
		t.Fatal("expected one delegated agent")
	}
}
