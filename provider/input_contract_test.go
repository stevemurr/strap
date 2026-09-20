package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/provider/vllm"
	"github.com/stevemurr/strap/tool"
)

func TestProvidersPreserveCanonicalContractAndArguments(t *testing.T) {
	type input struct {
		Value *string `json:"value"`
	}
	p, err := tool.NewParameters[input](tool.Nullable("value", "no value"))
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"input":{"value":"null"}}`)
	schema := p.Schema()
	same := func(a, b json.RawMessage) bool {
		var x, y any
		return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
	}
	for _, kind := range []string{"vllm", "chatcompletions"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					ToolChoice string `json:"tool_choice"`
					Tools      []struct {
						Function struct {
							Parameters json.RawMessage `json:"parameters"`
							Strict     bool            `json:"strict"`
						} `json:"function"`
					} `json:"tools"`
					Messages []struct {
						ToolCalls []struct {
							Function struct {
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"messages"`
				}
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
					return
				}
				if (kind == "vllm" && body.ToolChoice != "auto") || len(body.Tools) != 1 || !body.Tools[0].Function.Strict || !same(body.Tools[0].Function.Parameters, schema) {
					t.Error("provider changed advertised contract")
				}
				if len(body.Messages) < 1 || len(body.Messages[0].ToolCalls) != 1 || !same([]byte(body.Messages[0].ToolCalls[0].Function.Arguments), args) {
					t.Error("provider changed history arguments")
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"new","type":"function","function":{"name":"probe","arguments":"{\"input\":{\"value\":\"null\"}}"}}]},"finish_reason":"tool_calls"}]}`)
			}))
			defer server.Close()
			var client provider.Provider
			var e error
			if kind == "vllm" {
				client, e = vllm.New(vllm.Config{BaseURL: server.URL, Model: "m"})
			} else {
				client, e = chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "m"})
			}
			if e != nil {
				t.Fatal(e)
			}
			r, e := client.Submit(context.Background(), provider.Request{Tools: []provider.ToolDefinition{{Name: "probe", Parameters: schema}}, Messages: []provider.Message{{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "old", Name: "probe", Arguments: args}}}}}, nil)
			if e != nil || len(r.ToolCalls) != 1 || !same(r.ToolCalls[0].Arguments, args) {
				t.Fatalf("response changed: %+v %v", r, e)
			}
		})
	}
}
