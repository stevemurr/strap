package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"github.com/stevemurr/strap/provider/vllm"
)

func TestCLIProfilesAndOverridesReachHTTP(t *testing.T) {
	qwen := map[string]any{
		"temperature": 1.0, "top_p": 0.95, "top_k": 20.0, "min_p": 0.0,
		"presence_penalty": 0.0, "repetition_penalty": 1.1, "max_tokens": 81920.0,
		"chat_template_kwargs": map[string]any{"enable_thinking": true},
	}
	qwenNoThink := map[string]any{
		"temperature": 0.6, "top_p": 0.95, "top_k": 20.0, "min_p": 0.0,
		"presence_penalty": 0.0, "repetition_penalty": 1.0, "max_tokens": 131072.0,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	// A profile without a generation block leaves every setting to the server;
	// flags then add exactly what they name.
	bare := writeCatalog(t, `{"default":"bare","models":{"bare":{"model":"bare","base_url":"http://bare.test"}}}`)
	for _, tc := range []struct {
		name    string
		args    []string
		want    map[string]any
		generic bool
	}{
		{"nemotron profile", []string{"-profile", "nemotron-lightning"}, map[string]any{"temperature": 1.0, "top_p": 0.95, "chat_template_kwargs": map[string]any{"enable_thinking": true, "force_nonempty_content": true}}, false},
		{"qwen profile", []string{"-profile", "qwen3.6"}, qwen, false},
		{"qwen without thinking", []string{"-profile", "qwen3.6-nothink"}, qwenNoThink, false},
		{"flash next nothink", []string{"-profile", "qwen3.8-flash-next-nothink"}, map[string]any{
			"temperature": 0.7, "top_p": 0.8, "max_tokens": 32000.0, "chat_template_kwargs": map[string]any{"enable_thinking": false},
		}, false},
		{"flash next thinking", []string{"-profile", "qwen3.8-flash-next-thinking"}, map[string]any{
			"temperature": 1.0, "top_p": 0.95, "max_tokens": 32000.0, "chat_template_kwargs": map[string]any{"enable_thinking": true, "reasoning_effort": "medium"},
		}, false},
		{"flash next stream", []string{"-profile", "qwen3.8-flash-next-stream"}, map[string]any{
			"max_tokens": 400.0, "chat_template_kwargs": map[string]any{"enable_thinking": false},
		}, false},
		{"flash next effort override", []string{"-profile", "qwen3.8-flash-next-thinking", "-reasoning-effort", "low"}, map[string]any{
			"temperature": 1.0, "top_p": 0.95, "max_tokens": 32000.0, "chat_template_kwargs": map[string]any{"enable_thinking": true, "reasoning_effort": "low"},
		}, false},
		{"effort only", []string{"-config", bare, "-reasoning-effort", "xhigh"}, map[string]any{
			"chat_template_kwargs": map[string]any{"reasoning_effort": "xhigh"},
		}, false},
		{"server defaults", []string{"-config", bare}, map[string]any{}, false},
		{"zero and false overrides", []string{"-profile", "qwen3.6", "-temperature", "0", "-thinking=false", "-max-tokens", "4096"}, map[string]any{
			"temperature": 0.0, "top_p": 0.95, "top_k": 20.0, "min_p": 0.0, "presence_penalty": 0.0,
			"repetition_penalty": 1.1, "max_tokens": 4096.0, "chat_template_kwargs": map[string]any{"enable_thinking": false},
		}, false},
		{"overrides without saved settings", []string{"-config", bare, "-top-p", "0.8", "-top-k", "-1", "-min-p", "0", "-presence-penalty", "0", "-repetition-penalty", "1.1", "-thinking"}, map[string]any{
			"top_p": 0.8, "top_k": -1.0, "min_p": 0.0, "presence_penalty": 0.0, "repetition_penalty": 1.1,
			"chat_template_kwargs": map[string]any{"enable_thinking": true},
		}, false},
		{"generic", []string{"-backend", "chatcompletions"}, map[string]any{}, true},
		{"content only", []string{"-config", bare, "-force-nonempty-content"}, map[string]any{
			"chat_template_kwargs": map[string]any{"force_nonempty_content": true},
		}, false},
		{"explicit false content", []string{"-config", bare, "-force-nonempty-content=false"}, map[string]any{
			"chat_template_kwargs": map[string]any{"force_nonempty_content": false},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["model"] != "arbitrary-server-alias" {
					t.Error("application rewrote model alias")
				}
				delete(body, "model")
				delete(body, "messages")
				if body["stream"] != true {
					t.Error("streaming must be enabled")
				}
				delete(body, "stream")
				delete(body, "stream_options")
				if !reflect.DeepEqual(body, tc.want) {
					t.Errorf("got %#v, want %#v", body, tc.want)
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			args := append([]string{"-config", catalogPath, "-base-url", server.URL, "-model", "arbitrary-server-alias"}, tc.args...)
			o, err := parseOptions(args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			p, err := o.config.Model.NewProvider(server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if tc.generic {
				if _, ok := p.(*chatcompletions.Client); !ok {
					t.Fatal("wrong backend")
				}
			} else if _, ok := p.(*vllm.Client); !ok {
				t.Fatal("wrong backend")
			}
			if _, err := p.Submit(context.Background(), provider.Request{}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func valuePtr[T any](v T) *T { return &v }
