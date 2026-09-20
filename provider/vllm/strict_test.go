package vllm_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

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
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m", Generation: vllm.Generation{}})
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
