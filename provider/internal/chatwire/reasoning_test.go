package chatwire_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/internal/chatwire"
)

func TestReasoningWireNormalization(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		for _, tc := range []struct {
			name, fields, finish string
			bad, emitted         bool
		}{
			{"primary", `"reasoning":"why",`, "stop", false, true},
			{"alternate", `"reasoning_content":"why",`, "stop", false, true},
			{"identical", `"reasoning":"why","reasoning_content":"why",`, "stop", false, true},
			{"conflict", `"reasoning":"why","reasoning_content":"other",`, "stop", true, false},
			{"invalid field", `"reasoning":123,`, "stop", true, false},
			{"length", `"reasoning":"why",`, "length", true, true},
		} {
			t.Run(tc.name+map[bool]string{true: "/stream", false: "/json"}[streamed], func(t *testing.T) {
				body := `{"choices":[{"message":{"role":"assistant",` + tc.fields + `"content":"answer"},"finish_reason":"` + tc.finish + `"}],"usage":{"completion_tokens":9}}`
				mime := "application/json"
				if streamed {
					mime = "text/event-stream"
					body = "data: " + `{"choices":[{"index":0,"delta":{"role":"assistant",` + tc.fields + `"content":"answer"},"finish_reason":"` + tc.finish + `"}]}` + "\n\ndata: " + `{"choices":[],"usage":{"completion_tokens":9}}` + "\n\ndata: [DONE]\n\n"
				}
				c, err := chatwire.New("", "https://model.test", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				var deltas []provider.Delta
				response, err := c.Submit(context.Background(), struct{}{}, provider.ObserverFunc(func(d provider.Delta) error { deltas = append(deltas, d); return nil }))
				if (err != nil) != tc.bad {
					t.Fatal(response, err)
				}
				if tc.emitted {
					want := []provider.Delta{{Channel: provider.ChannelReasoning, Text: "why"}, {Channel: provider.ChannelContent, Text: "answer"}}
					if !reflect.DeepEqual(deltas, want) {
						t.Fatal(deltas)
					}
					if response.Usage == nil || *response.Usage.OutputTokens != 9 {
						t.Fatal(response)
					}
				} else if len(deltas) != 0 {
					t.Fatal("malformed record emitted text", deltas)
				}
				if !tc.bad && (response.Reasoning != "why" || response.Content != "answer") {
					t.Fatal(response)
				}
				if tc.bad && (response.Content != "" || response.Reasoning != "" || len(response.ToolCalls) != 0) {
					t.Fatal("failed response accepted", response)
				}
			})
		}
	}
}

func TestReasoningObserverFailureStopsCompleteJSON(t *testing.T) {
	sentinel := errors.New("capture failed")
	calls := 0
	_, err := client(t, `{"choices":[{"message":{"role":"assistant","reasoning":"why","content":"answer"},"finish_reason":"stop"}]}`).Submit(context.Background(), struct{}{}, provider.ObserverFunc(func(d provider.Delta) error {
		calls++
		if d.Channel != provider.ChannelReasoning {
			t.Fatal(d)
		}
		return sentinel
	}))
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatal(calls, err)
	}
}
