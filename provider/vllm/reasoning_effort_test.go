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

func TestReasoningEffortSnapshotAndTokenization(t *testing.T) {
	for _, effort := range []string{"low", "medium", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				want := fmt.Sprintf(`{"reasoning_effort":%q}`, effort)
				if string(body["chat_template_kwargs"]) != want || body["reasoning_effort"] != nil {
					t.Errorf("incorrect effort placement: %s", body)
				}
				if r.URL.Path == "/tokenize" {
					fmt.Fprint(w, `{"count":1}`)
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","reasoning_content":"reasoning","content":"answer"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			value := effort
			client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "local", Generation: vllm.Generation{ReasoningEffort: &value}})
			if err != nil {
				t.Fatal(err)
			}
			value = "changed after construction"
			if _, err := client.CountTokens(context.Background(), provider.Request{}); err != nil {
				t.Fatal(err)
			}
			got, err := client.Submit(context.Background(), provider.Request{}, nil)
			if err != nil || got.Content != "answer" || got.Reasoning != "reasoning" {
				t.Fatalf("reasoning and answer: %+v, %v", got, err)
			}
		})
	}
}

func TestInvalidReasoningEffort(t *testing.T) {
	for _, effort := range []string{"", "high", "LOW"} {
		if _, err := vllm.New(vllm.Config{BaseURL: "http://localhost:8365", Model: "local", Generation: vllm.Generation{ReasoningEffort: &effort}}); err == nil {
			t.Errorf("accepted effort %q", effort)
		}
	}
}
