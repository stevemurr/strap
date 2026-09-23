package vllm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

func TestPreserveThinkingHistoryAndTokenization(t *testing.T) {
	for _, name := range []string{"default", "false", "true"} {
		t.Run(name, func(t *testing.T) {
			var preserve *bool
			if name != "default" {
				preserve = ptr(name == "true")
			}
			var countedMessages json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				wantTemplate := ""
				if name != "default" {
					wantTemplate = fmt.Sprintf(`{"preserve_thinking":%s}`, name)
				}
				if string(body["chat_template_kwargs"]) != wantTemplate || body["preserve_thinking"] != nil {
					t.Errorf("incorrect template settings: %s", body)
				}
				var messages []map[string]json.RawMessage
				if err := json.Unmarshal(body["messages"], &messages); err != nil || len(messages) != 6 {
					t.Errorf("invalid history: %s, %v", body["messages"], err)
					return
				}
				for i, m := range messages {
					want := ""
					switch i {
					case 1:
						want = `"earlier reasoning 🌎"`
					case 3:
						want = `"tool reasoning"`
					}
					if string(m["reasoning"]) != want || string(m["reasoning_content"]) != want {
						t.Errorf("message %d lost reasoning or emitted it for an empty/non-assistant message: %s", i, m)
					}
				}
				if string(messages[1]["content"]) != `"answer"` || string(messages[3]["content"]) != `""` || messages[3]["tool_calls"] == nil || string(messages[4]["tool_call_id"]) != `"call"` {
					t.Errorf("reasoning changed answer/tool framing: %s", body["messages"])
				}
				if r.URL.Path == "/tokenize" {
					countedMessages = body["messages"]
					fmt.Fprint(w, `{"count":42}`)
				} else {
					if string(countedMessages) != string(body["messages"]) {
						t.Error("generation and token counting used different history")
					}
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "qwen", Generation: vllm.Generation{PreserveThinking: preserve}})
			if err != nil {
				t.Fatal(err)
			}
			if preserve != nil {
				*preserve = !*preserve // Both endpoints must use the constructor's snapshot.
			}
			input := provider.Request{Messages: []provider.Message{
				{Role: "user", Content: content.Text("first"), Reasoning: "ignore user reasoning"},
				{Role: "assistant", Content: content.Text("answer"), Reasoning: "earlier reasoning 🌎"},
				{Role: "user", Content: content.Text("second")},
				{Role: "assistant", Reasoning: "tool reasoning", ToolCalls: []provider.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}}},
				{Role: "tool", ToolCallID: "call", Content: content.Text("result"), Reasoning: "ignore tool reasoning"},
				{Role: "assistant", Content: content.Text("no reasoning")},
			}}
			original := provider.CopyMessages(input.Messages)
			if count, err := client.CountTokens(context.Background(), input); err != nil || count != 42 {
				t.Fatalf("count = %d, %v", count, err)
			}
			if _, err := client.Submit(context.Background(), input, nil); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input.Messages, original) {
				t.Fatal("provider mutated history")
			}
		})
	}
}
