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
		all       bool
		want      map[string]bool
	}{
		{"unset", nil, false, nil},
		{"strict all", nil, true, map[string]bool{"shell": true, "read_file": true}},
		{"all takes precedence", []string{"create_plan"}, true, map[string]bool{"shell": true, "read_file": true}},
		{"one tool", []string{"shell"}, false, map[string]bool{"shell": true}},
		{"every tool", []string{"shell", "read_file"}, false, map[string]bool{"shell": true, "read_file": true}},
		{"tool this role does not advertise", []string{"create_plan"}, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					StrictTools    []string `json:"strict_tools"`
					StrictAllTools *bool    `json:"strict_all_tools"`
					ToolChoice     string   `json:"tool_choice"`
					Tools          []struct {
						Function struct {
							Name   string `json:"name"`
							Strict *bool  `json:"strict"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.ToolChoice != "auto" {
					t.Errorf("tool_choice=%q, want auto", body.ToolChoice)
				}
				if body.StrictAllTools != nil {
					t.Error("strict_all_tools leaked into request")
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
			client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m", Generation: vllm.Generation{StrictTools: c.configure, StrictAllTools: c.all}})
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

// Dynamic tools are marked on each request, not snapshotted at construction.
func TestStrictAllToolsIncludesNewToolsOnLaterRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ToolChoice string `json:"tool_choice"`
			Tools      []struct {
				Function struct {
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests++
		if len(body.Tools) != requests || body.ToolChoice != "auto" {
			t.Errorf("unexpected tools/choice: %+v", body)
		}
		for _, tool := range body.Tools {
			if !tool.Function.Strict {
				t.Errorf("%s is not strict", tool.Function.Name)
			}
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m", Generation: vllm.Generation{StrictAllTools: true}})
	if err != nil {
		t.Fatal(err)
	}
	r := provider.Request{}
	for _, name := range []string{"wait_for_input", "new_extension_tool"} {
		r.Tools = append(r.Tools, provider.ToolDefinition{Name: name, Parameters: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)})
		if _, err := client.Submit(t.Context(), r, nil); err != nil {
			t.Fatal(err)
		}
	}
}
