package chatwire_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

func TestRejectedArgumentsPreserveOriginalProtocolString(t *testing.T) {
	for _, raw := range []string{"", " \t{\"message\":\"hé\\nllo\"\n", "\n[]\t", "null", `"quoted"`, "{}\n{}"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/stream=%t", raw, streaming), func(t *testing.T) {
				valid := map[string]any{"id": "valid", "type": "function", "function": map[string]any{"name": "read", "arguments": "{}"}}
				rejected := map[string]any{"id": "rejected", "type": "function", "function": map[string]any{"name": "send_message", "arguments": raw}}
				usage := map[string]int{"prompt_tokens": 10, "completion_tokens": 20}
				var body strings.Builder
				contentType := "application/json"
				if streaming {
					contentType = "text/event-stream"
					valid["index"] = 0
					runes := []rune(raw)
					for i, fragment := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
						call := map[string]any{"index": 1, "function": map[string]any{"arguments": fragment}}
						delta := map[string]any{"tool_calls": []any{call}}
						choice := map[string]any{"index": 0, "delta": delta}
						if i == 0 {
							call["id"], call["type"] = "rejected", "function"
							call["function"].(map[string]any)["name"] = "send_message"
							delta["role"], delta["content"] = "assistant", "not accepted"
							delta["tool_calls"] = []any{valid, call}
						} else {
							choice["finish_reason"] = "tool_calls"
						}
						b, _ := json.Marshal(map[string]any{"choices": []any{choice}})
						fmt.Fprintf(&body, "data: %s\n\n", b)
					}
					b, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": usage})
					fmt.Fprintf(&body, "data: %s\n\ndata: [DONE]\n\n", b)
				} else {
					b, _ := json.Marshal(map[string]any{"usage": usage, "choices": []any{map[string]any{
						"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": "not accepted", "tool_calls": []any{valid, rejected}},
					}}})
					body.Write(b)
				}
				c, err := chatwire.New("https://model.test", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body.String()))}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				response, err := c.Submit(context.Background(), struct{}{}, nil)
				var detail *provider.ToolArgumentsError
				if !errors.As(err, &detail) || detail.CallID != "rejected" || detail.Name != "send_message" || detail.Arguments != raw {
					t.Fatalf("lost rejected bytes: detail=%+v error=%v", detail, err)
				}
				if len(response.ToolCalls) != 0 || response.Content != "" || response.Usage == nil || *response.Usage.OutputTokens != 20 {
					t.Fatalf("rejected response escaped or accounting lost: %+v", response)
				}
			})
		}
	}
}
