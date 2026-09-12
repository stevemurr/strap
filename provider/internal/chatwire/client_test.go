package chatwire_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stevemurr/strap/content"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func client(t *testing.T, body string) *chatwire.Client {
	t.Helper()
	c, err := chatwire.New("https://model.test", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestURLValidationAndDefaultClient(t *testing.T) {
	for _, url := range []string{"", ":bad", "relative", "ftp://host", "https://user:pass@host", "https://host?x=1", "https://host#fragment"} {
		if _, err := chatwire.New(url, nil); err == nil {
			t.Errorf("accepted %q", url)
		}
	}
	if _, err := chatwire.New("https://model.test/v1/", nil); err != nil {
		t.Fatal(err)
	}
}
func TestRequestTranslationAndTransport(t *testing.T) {
	image := content.Part{Image: &content.Image{MIMEType: "image/png", Data: []byte{1, 2, 3}}}
	request, err := chatwire.Encode("model", provider.Request{Messages: []provider.Message{
		{Role: "system", Content: content.Text("system")},
		{Role: "user", Content: content.Content{{Text: "inspect"}, image}},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read", Arguments: json.RawMessage(`{}`)}, {ID: "c2", Name: "read", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "c1", Content: content.Content{{Text: "page"}, image}},
		{Role: "tool", ToolCallID: "c2", Content: content.Text("second")},
		{Role: "assistant", Content: content.Text("done")},
	}, Tools: []provider.ToolDefinition{{Name: "read", Description: "read pages", Parameters: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := chatwire.New("https://model.test/api/", &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://model.test/api/chat/completions" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request: %+v", r)
		}
		var body struct {
			Model    string
			Stream   bool
			Messages []struct {
				Role       string
				Content    json.RawMessage
				ToolCallID string `json:"tool_call_id"`
			}
			Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "model" || body.Stream || len(body.Messages) != 7 || len(body.Tools) != 1 {
			t.Errorf("wire: %+v", body)
		}
		if body.Messages[3].Role != "tool" || body.Messages[4].ToolCallID != "c2" || body.Messages[5].Role != "user" || !strings.Contains(string(body.Messages[5].Content), "data:image/png;base64,AQID") || !strings.Contains(string(body.Messages[5].Content), "tool call c1") {
			t.Errorf("image attribution/order: %+v", body.Messages)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Submit(context.Background(), request)
	if err != nil || response.Content != "done" {
		t.Fatalf("%+v %v", response, err)
	}
	for _, part := range []content.Part{{Image: &content.Image{}}, image} {
		if _, err := chatwire.Encode("model", provider.Request{Messages: []provider.Message{{Role: "assistant", Content: content.Content{part}}}}); err == nil {
			t.Fatal("accepted invalid assistant image")
		}
	}
}
func TestCompletionValidationAndUsage(t *testing.T) {
	goodCall := `{"id":"c","type":"function","function":{"name":"read","arguments":" {\"path\":\"x\"} "}}`
	for _, tc := range []struct {
		name, choices, usage string
		invalid              bool
	}{
		{"text", `[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]`, `{"prompt_tokens":0,"completion_tokens":7}`, false},
		{"tool", `[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[` + goodCall + `]}}]`, `{"prompt_tokens":8,"completion_tokens":-1}`, false},
		{"no choices", `[]`, `null`, true},
		{"length", `[{"finish_reason":"length"}]`, `{"prompt_tokens":8}`, true},
		{"wrong role", `[{"finish_reason":"stop","message":{"role":"user","content":"bad"}}]`, `[]`, true},
		{"empty", `[{"finish_reason":"stop","message":{"role":"assistant","content":"  "}}]`, `{}`, true},
		{"missing tools", `[{"finish_reason":"tool_calls","message":{"role":"assistant"}}]`, `{"completion_tokens":"bad"}`, true},
		{"duplicate", `[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[` + goodCall + `,` + goodCall + `]}}]`, `null`, true},
		{"missing identity", `[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"type":"function"}]}}]`, `null`, true},
		{"invalid arguments", `[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":"{"}}]}}]`, `null`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := client(t, `{"choices":`+tc.choices+`,"usage":`+tc.usage+`}`).Submit(context.Background(), struct{}{})
			if (err != nil) != tc.invalid {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			switch tc.name {
			case "text":
				if response.Content != "done" || response.Usage == nil || *response.Usage.InputTokens != 0 || *response.Usage.OutputTokens != 7 {
					t.Fatal(response)
				}
			case "tool":
				if len(response.ToolCalls) != 1 || string(response.ToolCalls[0].Arguments) != `{"path":"x"}` || response.Usage.OutputTokens != nil {
					t.Fatal(response)
				}
			case "length":
				if response.Usage == nil || *response.Usage.InputTokens != 8 || response.Content != "" {
					t.Fatal("lost truncation usage", response)
				}
			}
		})
	}
}
func TestTransportFailures(t *testing.T) {
	c := client(t, `invalid`)
	if _, err := c.Submit(context.Background(), struct{}{}); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatal(err)
	}
	if err := c.Post(context.Background(), "https://model.test", make(chan int), nil); err == nil || !strings.Contains(err.Error(), "encode request") {
		t.Fatal(err)
	}
	if err := c.Post(context.Background(), ":invalid", nil, nil); err == nil {
		t.Fatal("invalid URL accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failing, _ := chatwire.New("https://model.test", &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })})
	if _, err := failing.Submit(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	rejecting, _ := chatwire.New("https://model.test", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 5000)))}, nil
	})})
	_, err := rejecting.Submit(context.Background(), nil)
	var status *chatwire.HTTPError
	if !errors.As(err, &status) || status.StatusCode != 429 || len(status.Body) != 4096 || !strings.HasPrefix(err.Error(), "HTTP 429:") {
		t.Fatal(err)
	}
}
