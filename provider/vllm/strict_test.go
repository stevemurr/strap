package vllm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

// StrictTools is applied to each advertised tool, never sent as a sampling
// field, and absent unless configured, so servers without support see no change.
func TestStrictToolsMarksEveryAdvertisedTool(t *testing.T) {
	for _, strict := range []*bool{nil, ptr(false), ptr(true)} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					StrictTools *bool `json:"strict_tools"`
					Tools       []struct {
						Function struct {
							Name   string `json:"name"`
							Strict *bool  `json:"strict"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.StrictTools != nil {
					t.Error("strict_tools leaked into the request body")
				}
				if len(body.Tools) != 2 {
					t.Fatalf("tools: %+v", body.Tools)
				}
				for _, tool := range body.Tools {
					want := strict != nil && *strict
					if (tool.Function.Strict != nil) != want || (want && !*tool.Function.Strict) {
						t.Errorf("%s strict=%v, want present=%v", tool.Function.Name, tool.Function.Strict, want)
					}
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m", Generation: vllm.Generation{StrictTools: strict}})
			if err != nil {
				t.Fatal(err)
			}
			request := provider.Request{Tools: []provider.ToolDefinition{
				{Name: "shell", Parameters: json.RawMessage(`{"type":"object"}`)},
				{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)},
			}}
			if _, err := client.Submit(context.Background(), request, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
