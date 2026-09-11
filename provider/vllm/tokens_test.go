package vllm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

func TestCountTokensMatchesGenerationPrompt(t *testing.T) {
	for _, thinking := range []*bool{nil, ptr(false), ptr(true)} {
		t.Run(fmt.Sprint(thinking), func(t *testing.T) {
			bodies := make(map[string]map[string]json.RawMessage)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected request: %s %v", r.Method, r.Header)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies[r.URL.Path] = body
				switch r.URL.Path {
				case "/tokenize":
					fmt.Fprint(w, `{"count":123,"max_model_len":262144,"tokens":[1,2,3]}`)
				case "/v1/chat/completions":
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client, err := vllm.New(vllm.Config{
				BaseURL: server.URL, Model: "local", HTTPClient: server.Client(),
				Generation: vllm.Generation{EnableThinking: thinking, Temperature: ptr(0.6), MaxTokens: ptr(100)},
			})
			if err != nil {
				t.Fatal(err)
			}
			// Both operations must retain the constructor's snapshot.
			var wantThinking string
			if thinking != nil {
				wantThinking = fmt.Sprintf(`{"enable_thinking":%t}`, *thinking)
				*thinking = !*thinking
			}
			input := provider.Request{
				Agent: "private-agent",
				Messages: []provider.Message{
					{Role: "system", Content: content.Text("Be helpful.")},
					{Role: "user", Content: content.Text("こんにちは 🌍"), Envelope: &message.Message{ID: "private-envelope"}},
					{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "inspect", Arguments: json.RawMessage(`{"path":"a"}`)}}},
					{Role: "tool", ToolCallID: "call-1", Content: content.Content{
						{Text: "image result"}, {Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
					}},
				},
				Tools: []provider.ToolDefinition{{Name: "inspect", Description: "Inspect a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
			}
			original := provider.CopyMessages(input.Messages)
			var counter provider.TokenCounter = client
			if n, err := counter.CountTokens(context.Background(), input); err != nil || n != 123 {
				t.Fatalf("CountTokens = %d, %v", n, err)
			}
			if len(bodies) != 1 {
				t.Fatal("counting also requested generation")
			}
			if _, err := client.Submit(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			count, submit := bodies["/tokenize"], bodies["/v1/chat/completions"]
			for _, field := range []string{"model", "messages", "tools", "chat_template_kwargs"} {
				if string(count[field]) != string(submit[field]) {
					t.Errorf("%s differs: count=%s submit=%s", field, count[field], submit[field])
				}
			}
			if string(count["chat_template_kwargs"]) != wantThinking || string(count["add_generation_prompt"]) != "true" {
				t.Errorf("incorrect template settings: %s", count)
			}
			for field := range count {
				switch field {
				case "model", "messages", "tools", "chat_template_kwargs", "add_generation_prompt":
				default:
					t.Errorf("unexpected tokenize field: %s", field)
				}
			}
			if strings.Contains(string(count["messages"]), "private-") || !reflect.DeepEqual(input.Messages, original) {
				t.Fatal("counting leaked metadata or changed the request")
			}
		})
	}
}

func TestCountTokensEndpointPaths(t *testing.T) {
	for _, tc := range []struct{ base, path string }{
		{"", "/tokenize"}, {"/", "/tokenize"}, {"/v1", "/tokenize"}, {"/v1/", "/tokenize"},
		{"/proxy", "/proxy/tokenize"}, {"/proxy/v1/", "/proxy/tokenize"},
		{"/v10", "/v10/tokenize"}, {"/a%20b/v1", "/a b/tokenize"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				fmt.Fprint(w, `{"count":0}`)
			}))
			defer server.Close()
			client, err := vllm.New(vllm.Config{BaseURL: server.URL + tc.base, Model: "local"})
			if err != nil {
				t.Fatal(err)
			}
			if n, err := client.CountTokens(context.Background(), provider.Request{}); err != nil || n != 0 {
				t.Fatalf("CountTokens = %d, %v", n, err)
			}
		})
	}
}

func TestCountTokensValidatesResponse(t *testing.T) {
	for _, tc := range []struct {
		body  string
		want  int64
		valid bool
	}{
		{`{"count":0}`, 0, true}, {`{"count":9223372036854775807}`, math.MaxInt64, true},
		{`{}`, 0, false}, {`null`, 0, false}, {`{"count":null}`, 0, false},
		{`{"count":-1}`, 0, false}, {`{"count":1.5}`, 0, false},
		{`{"count":"42"}`, 0, false}, {`{"count":true}`, 0, false},
		{`{"count":9223372036854775808}`, 0, false}, {`{"count":`, 0, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "local"})
			if err != nil {
				t.Fatal(err)
			}
			n, err := client.CountTokens(context.Background(), provider.Request{})
			if (err == nil) != tc.valid || n != tc.want {
				t.Fatalf("CountTokens = %d, %v", n, err)
			}
		})
	}
}

func TestCountTokensErrorsAndCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "tokenization unavailable: "+strings.Repeat("x", 5000))
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := client.CountTokens(context.Background(), provider.Request{})
	var responseError *vllm.HTTPError
	if n != 0 || !errors.As(err, &responseError) || responseError.StatusCode != 404 || len(responseError.Body) != 4096 || !strings.HasPrefix(responseError.Body, "tokenization unavailable:") {
		t.Fatalf("lost HTTP diagnostic: %d, %v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CountTokens(ctx, provider.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	badInputs := []provider.Request{
		{Messages: []provider.Message{{Role: "user", Content: content.Content{{Image: &content.Image{MIMEType: "image/png"}}}}}},
		{Tools: []provider.ToolDefinition{{Name: "bad", Parameters: json.RawMessage(`{`)}}},
	}
	for _, input := range badInputs {
		if _, err := client.CountTokens(context.Background(), input); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected requests or retries: %d", calls.Load())
	}
}

func TestCountTokensConcurrentRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Messages) != 1 {
			t.Error("request history leaked")
			return
		}
		fmt.Fprintf(w, `{"count":%s}`, body.Messages[0].Content)
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := client.CountTokens(context.Background(), provider.Request{Messages: []provider.Message{{Role: "user", Content: content.Text(fmt.Sprint(i))}}})
			if err != nil || n != int64(i) {
				t.Errorf("request %d: count=%d, err=%v", i, n, err)
			}
		}()
	}
	wg.Wait()
}
