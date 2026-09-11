package provider_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/provider/vllm"
)

func tokenCount(n int64) *int64 { return &n }

func TestAdaptersReportUsage(t *testing.T) {
	const valid = `[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]`
	for _, adapter := range []struct {
		name string
		new  func(string) (provider.Provider, error)
	}{
		{"chatcompletions", func(url string) (provider.Provider, error) {
			return chatcompletions.New(chatcompletions.Config{BaseURL: url, Model: "test"})
		}},
		{"vllm", func(url string) (provider.Provider, error) {
			return vllm.New(vllm.Config{BaseURL: url, Model: "test"})
		}},
	} {
		t.Run(adapter.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, usage, choices string
				want                 *provider.Usage
				wantError            bool
			}{
				{name: "reported", usage: `{"prompt_tokens":1000,"completion_tokens":25,"total_tokens":1025}`, want: &provider.Usage{InputTokens: tokenCount(1000), OutputTokens: tokenCount(25)}},
				{name: "omitted"},
				{name: "null", usage: `null`},
				{name: "empty", usage: `{}`},
				{name: "zero", usage: `{"prompt_tokens":0,"completion_tokens":0}`, want: &provider.Usage{InputTokens: tokenCount(0), OutputTokens: tokenCount(0)}},
				{name: "partial", usage: `{"completion_tokens":8}`, want: &provider.Usage{OutputTokens: tokenCount(8)}},
				{name: "large", usage: `{"prompt_tokens":3000000000}`, want: &provider.Usage{InputTokens: tokenCount(3000000000)}},
				{name: "negative", usage: `{"prompt_tokens":-1,"completion_tokens":4}`, want: &provider.Usage{OutputTokens: tokenCount(4)}},
				{name: "wrong type", usage: `{"prompt_tokens":"100","completion_tokens":4}`, want: &provider.Usage{OutputTokens: tokenCount(4)}},
				{name: "fractional", usage: `{"prompt_tokens":1.5}`},
				{name: "overflow", usage: `{"prompt_tokens":9223372036854775808}`},
				{name: "malformed usage", usage: `[]`},
				{name: "null count", usage: `{"prompt_tokens":null}`},
				{name: "truncated", usage: `{"prompt_tokens":100,"completion_tokens":20}`, choices: `[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]`, want: &provider.Usage{InputTokens: tokenCount(100), OutputTokens: tokenCount(20)}, wantError: true},
				{name: "invalid tool", usage: `{"completion_tokens":20}`, choices: `[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"shell","arguments":"{"}}]}}]`, want: &provider.Usage{OutputTokens: tokenCount(20)}, wantError: true},
				{name: "missing choice", usage: `{"prompt_tokens":10}`, choices: `[]`, want: &provider.Usage{InputTokens: tokenCount(10)}, wantError: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					choices := tc.choices
					if choices == "" {
						choices = valid
					}
					body := `{"choices":` + choices
					if tc.usage != "" {
						body += `,"usage":` + tc.usage
					}
					body += `}`
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						fmt.Fprint(w, body)
					}))
					defer server.Close()
					client, err := adapter.new(server.URL)
					if err != nil {
						t.Fatal(err)
					}
					got, err := client.Submit(context.Background(), provider.Request{})
					if (err != nil) != tc.wantError || !reflect.DeepEqual(got.Usage, tc.want) {
						t.Fatalf("usage = %+v, err = %v; want %+v, error %v", got.Usage, err, tc.want, tc.wantError)
					}
					if tc.wantError {
						if got.Content != "" || len(got.ToolCalls) != 0 {
							t.Fatal("rejected output escaped validation")
						}
					} else if got.Content != "done" {
						t.Fatalf("optional usage affected content: %+v", got)
					}
				})
			}
		})
	}
}
