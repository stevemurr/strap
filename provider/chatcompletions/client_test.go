package chatcompletions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
)

func TestToolCallRoundTrip(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected HTTP request: %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if string(request["model"]) != `"local-model"` || string(request["stream"]) != "false" {
			t.Errorf("bad request: %s", request)
		}
		if len(request) != 4 {
			t.Errorf("unexpected internal fields: %v", request)
		}
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(request["messages"], &messages); err != nil {
			t.Error(err)
			return
		}
		for _, m := range messages {
			if _, ok := m["Envelope"]; ok {
				t.Error("internal envelope leaked")
			}
			if _, ok := m["envelope"]; ok {
				t.Error("internal envelope leaked")
			}
		}
		var tools []struct {
			Type     string
			Function struct {
				Name       string
				Parameters map[string]any
			}
		}
		if err := json.Unmarshal(request["tools"], &tools); err != nil {
			t.Error(err)
			return
		}
		if len(tools) != 1 || tools[0].Type != "function" || tools[0].Function.Name != "create_agent" || tools[0].Function.Parameters["type"] != "object" {
			t.Errorf("bad tools: %+v", tools)
		}
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-42","type":"function","function":{"name":"create_agent","arguments":"{\"task\":\"hello\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		var assistant struct {
			ToolCalls []struct {
				ID       string
				Function struct {
					Name      string
					Arguments string
				}
			} `json:"tool_calls"`
		}
		encoded, _ := json.Marshal(messages[2])
		if err := json.Unmarshal(encoded, &assistant); err != nil {
			t.Error(err)
			return
		}
		if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call-42" || assistant.ToolCalls[0].Function.Arguments != `{"task":"hello"}` {
			t.Errorf("tool call lost during history translation: %+v", assistant)
		}
		if string(messages[3]["tool_call_id"]) != `"call-42"` || string(messages[3]["role"]) != `"tool"` {
			t.Errorf("tool result lost correlation: %v", messages[3])
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Delegated."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	c, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "local-model", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	request := provider.Request{
		Agent: "internal-agent-id",
		Messages: []provider.Message{
			{Role: "system", Content: content.Text("Coordinate.")},
			{Role: "user", Content: content.Text("hello"), Envelope: &message.Message{ID: "internal-message-id"}},
		},
		Tools: []provider.ToolDefinition{{Name: "create_agent", Parameters: json.RawMessage(`{"type":"object","properties":{"task":{"type":"string"}},"required":["task"],"additionalProperties":false}`)}},
	}
	response, err := c.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "call-42" || string(response.ToolCalls[0].Arguments) != `{"task":"hello"}` {
		t.Fatalf("bad response: %+v", response)
	}
	request.Messages = append(request.Messages,
		provider.Message{Role: "assistant", ToolCalls: response.ToolCalls},
		provider.Message{Role: "tool", ToolCallID: "call-42", Content: content.Text(`{"agent_id":"child","instruction":{"message_id":"assignment-1","recipient":"child","status":"queued"}}`)},
	)
	response, err = c.Submit(context.Background(), request)
	if err != nil || response.Content != "Delegated." {
		t.Fatalf("%+v %v", response, err)
	}
}

func TestAPIPrefixAndHTTPErrorWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Error(r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"model not loaded"}}`)
	}))
	defer server.Close()
	c, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL + "/v1/", Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Submit(context.Background(), provider.Request{})
	var responseError *chatcompletions.HTTPError
	if !errors.As(err, &responseError) || responseError.StatusCode != 503 || !strings.Contains(responseError.Body, "model not loaded") {
		t.Fatalf("lost server diagnostic: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("request retried")
	}
}

func TestIncompleteAndMalformedResponsesAreErrors(t *testing.T) {
	cases := map[string]string{
		"truncated":             `{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`,
		"no choice":             `{"choices":[]}`,
		"no output":             `{"choices":[{"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`,
		"invalid JSON":          `{`,
		"broken arguments":      `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"create_agent","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`,
		"missing call identity": `{"choices":[{"message":{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"create_agent","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			c, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "local"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Submit(context.Background(), provider.Request{}); err == nil {
				t.Fatal("invalid completion accepted")
			}
		})
	}
}

func TestCancellationReachesHTTPCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	c, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Submit(ctx, provider.Request{}); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP call did not begin")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP call ignored cancellation")
	}
}
