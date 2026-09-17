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

// StrictTools marks exactly the named tools, never leaks as a sampling field,
// and leaves every request untouched unless a name matches, so servers without
// support and roles without the named tool see no change.
func TestStrictToolsMarksOnlyTheNamedTools(t *testing.T) {
	for _, c := range []struct {
		name      string
		configure []string
		want      map[string]bool
	}{
		{"unset", nil, nil},
		{"one tool", []string{"shell"}, map[string]bool{"shell": true}},
		{"every tool", []string{"shell", "read_file"}, map[string]bool{"shell": true, "read_file": true}},
		{"tool this role does not advertise", []string{"create_plan"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					StrictTools []string `json:"strict_tools"`
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
					marked := tool.Function.Strict != nil && *tool.Function.Strict
					if marked != c.want[tool.Function.Name] || tool.Function.Strict != nil && !marked {
						t.Errorf("%s strict=%v, want %v", tool.Function.Name, tool.Function.Strict, c.want[tool.Function.Name])
					}
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m", Generation: vllm.Generation{StrictTools: c.configure}})
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

// A blank name cannot mark anything and is more likely a mis-split list than
// an intent, so construction reports it rather than silently doing nothing.
func TestStrictToolsRejectsBlankNames(t *testing.T) {
	if _, err := vllm.New(vllm.Config{BaseURL: "http://model.test", Model: "m", Generation: vllm.Generation{StrictTools: []string{"shell", " "}}}); err == nil {
		t.Fatal("accepted a blank strict tool name")
	}
}
