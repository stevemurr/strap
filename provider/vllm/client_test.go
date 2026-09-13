package vllm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

func ptr[T any](v T) *T { return &v }

func TestGenerationSnapshotAndConcurrentIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		want := map[string]string{
			"temperature": "0", "top_p": "0.95", "top_k": "20", "min_p": "0",
			"presence_penalty": "0", "repetition_penalty": "1", "max_tokens": "32768",
			"chat_template_kwargs": `{"enable_thinking":false,"force_nonempty_content":true}`,
		}
		if string(body["model"]) == `"second"` {
			want = map[string]string{
				"temperature": "0.8", "top_p": "0.8", "top_k": "0", "min_p": "0.1",
				"presence_penalty": "1.5", "repetition_penalty": "1.2", "max_tokens": "2048",
				"chat_template_kwargs": `{"enable_thinking":true,"force_nonempty_content":false}`,
			}
		}
		for name, expected := range want {
			if string(body[name]) != expected {
				t.Errorf("%s: got %s, want %s", name, body[name], expected)
			}
		}
		// Only protocol fields and typed generation options may cross the wire.
		for name := range body {
			if _, ok := want[name]; !ok && name != "model" && name != "messages" && name != "tools" && name != "stream" && name != "stream_options" {
				t.Errorf("unexpected wire field %s", name)
			}
		}
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Error(err)
			return
		}
		if len(messages) != 1 {
			t.Error("shared client retained conversation history")
			return
		}
		if len(messages[0]) != 2 || string(messages[0]["role"]) != `"user"` {
			t.Error("internal message metadata leaked")
		}
		if !strings.Contains(string(body["tools"]), `"name":"inspect"`) {
			t.Error("tool definition lost")
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, messages[0]["content"])
	}))
	defer server.Close()
	g := vllm.Generation{Temperature: ptr(0.0), TopP: ptr(0.95), TopK: ptr(20), MinP: ptr(0.0), PresencePenalty: ptr(0.0), RepetitionPenalty: ptr(1.0), MaxTokens: ptr(32768), EnableThinking: ptr(false), ForceNonemptyContent: ptr(true)}
	first, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "first", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	*g.Temperature, *g.TopP, *g.TopK, *g.MinP = 0.8, 0.8, 0, 0.1
	*g.PresencePenalty, *g.RepetitionPenalty, *g.MaxTokens, *g.EnableThinking = 1.5, 1.2, 2048, true
	*g.ForceNonemptyContent = false
	second, err := vllm.New(vllm.Config{BaseURL: server.URL + "/v1/", Model: "second", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		for _, client := range []*vllm.Client{first, second} {
			wg.Add(1)
			go func(i int, client *vllm.Client) {
				defer wg.Done()
				text := fmt.Sprintf("agent %d", i)
				got, err := client.Submit(context.Background(), provider.Request{
					Agent: message.ActorID(text), Messages: []provider.Message{{Role: "user", Content: content.Text(text), Envelope: &message.Message{ID: "private"}}},
					Tools: []provider.ToolDefinition{{Name: "inspect", Parameters: json.RawMessage(`{"type":"object"}`)}},
				}, nil)
				if err != nil || got.Content != text {
					t.Errorf("response lost correlation: %+v %v", got, err)
				}
			}(i, client)
		}
	}
	wg.Wait()
}

func TestUnsetGenerationUsesServerDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 4 || body["model"] == nil || body["messages"] == nil || string(body["stream"]) != "true" {
			t.Errorf("unexpected defaults: %s", body)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "any-model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Submit(context.Background(), provider.Request{}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidGenerationFailsAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    vllm.Generation
	}{
		{"temperature", vllm.Generation{Temperature: ptr(-0.1)}},
		{"temperature", vllm.Generation{Temperature: ptr(2.1)}},
		{"temperature", vllm.Generation{Temperature: ptr(math.NaN())}},
		{"top_p", vllm.Generation{TopP: ptr(0.0)}},
		{"top_p", vllm.Generation{TopP: ptr(1.1)}},
		{"top_p", vllm.Generation{TopP: ptr(math.NaN())}},
		{"top_k", vllm.Generation{TopK: ptr(-2)}},
		{"min_p", vllm.Generation{MinP: ptr(-0.1)}},
		{"min_p", vllm.Generation{MinP: ptr(1.1)}},
		{"min_p", vllm.Generation{MinP: ptr(math.Inf(1))}},
		{"presence_penalty", vllm.Generation{PresencePenalty: ptr(2.1)}},
		{"presence_penalty", vllm.Generation{PresencePenalty: ptr(-2.1)}},
		{"presence_penalty", vllm.Generation{PresencePenalty: ptr(math.NaN())}},
		{"repetition_penalty", vllm.Generation{RepetitionPenalty: ptr(0.0)}},
		{"repetition_penalty", vllm.Generation{RepetitionPenalty: ptr(math.Inf(1))}},
		{"max_tokens", vllm.Generation{MaxTokens: ptr(0)}},
	} {
		_, err := vllm.New(vllm.Config{BaseURL: "http://127.0.0.1:1", Model: "local", Generation: tc.g})
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	for _, g := range []vllm.Generation{
		{Temperature: ptr(0.0), TopP: ptr(1.0), TopK: ptr(-1), MinP: ptr(0.0), PresencePenalty: ptr(-2.0), RepetitionPenalty: ptr(0.1), MaxTokens: ptr(1)},
		{Temperature: ptr(2.0), TopK: ptr(0), MinP: ptr(1.0), PresencePenalty: ptr(2.0)},
	} {
		if _, err := vllm.New(vllm.Config{BaseURL: "http://127.0.0.1:1", Model: "local", Generation: g}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServerRejectionAndCancellationArePreserved(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"unsupported chat_template_kwargs"}`)
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "local", Generation: vllm.Generation{EnableThinking: ptr(true)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Submit(context.Background(), provider.Request{}, nil)
	var responseError *vllm.HTTPError
	if !errors.As(err, &responseError) || responseError.StatusCode != 400 || !strings.Contains(responseError.Body, "chat_template_kwargs") {
		t.Fatalf("lost server diagnostic: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("rejected settings retried")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Submit(ctx, provider.Request{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled request reached server")
	}
}
