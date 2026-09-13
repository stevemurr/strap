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

func TestCLIBackendPresetAndOverridesReachHTTP(t *testing.T) {
	preset := map[string]any{
		"temperature": 0.6, "top_p": 0.95, "top_k": 20.0, "min_p": 0.0,
		"presence_penalty": 0.0, "repetition_penalty": 1.0, "max_tokens": 131072.0,
		"chat_template_kwargs": map[string]any{"enable_thinking": true},
	}
	for _, tc := range []struct {
		name    string
		args    []string
		want    map[string]any
		generic bool
	}{
		{"default", nil, map[string]any{"temperature": 1.0, "top_p": 0.95, "chat_template_kwargs": map[string]any{"enable_thinking": true, "force_nonempty_content": true}}, false},
		{"qwen profile", []string{"-profile", "qwen3.6"}, preset, false},
		{"explicit preset", []string{"-preset", "qwen3.6-coding"}, preset, false},
		{"server defaults", []string{"-preset", "none"}, map[string]any{}, false},
		{"zero and false overrides", []string{"-profile", "qwen3.6", "-temperature", "0", "-thinking=false", "-max-tokens", "4096"}, map[string]any{
			"temperature": 0.0, "top_p": 0.95, "top_k": 20.0, "min_p": 0.0, "presence_penalty": 0.0,
			"repetition_penalty": 1.0, "max_tokens": 4096.0, "chat_template_kwargs": map[string]any{"enable_thinking": false},
		}, false},
		{"overrides without preset", []string{"-preset", "none", "-top-p", "0.8", "-top-k", "-1", "-min-p", "0", "-presence-penalty", "0", "-repetition-penalty", "1.1", "-thinking"}, map[string]any{
			"top_p": 0.8, "top_k": -1.0, "min_p": 0.0, "presence_penalty": 0.0, "repetition_penalty": 1.1,
			"chat_template_kwargs": map[string]any{"enable_thinking": true},
		}, false},
		{"generic", []string{"-backend", "chatcompletions"}, map[string]any{}, true},
		{"content only", []string{"-preset", "none", "-force-nonempty-content"}, map[string]any{
			"chat_template_kwargs": map[string]any{"force_nonempty_content": true},
		}, false},
		{"explicit false content", []string{"-preset", "none", "-force-nonempty-content=false"}, map[string]any{
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
				delete(body, "stream")
				delete(body, "stream_options")
				if !reflect.DeepEqual(body, tc.want) {
					t.Errorf("got %#v, want %#v", body, tc.want)
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			args := append([]string{"-config", "models.json", "-base-url", server.URL, "-model", "arbitrary-server-alias"}, tc.args...)
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

func TestCLIRejectsInvalidOrUnsupportedModelOptions(t *testing.T) {
	for _, args := range [][]string{
		{"-backend", "unknown"}, {"-preset", "unknown"},
		{"-backend", "chatcompletions", "-preset", "qwen3.6-coding"},
		{"-backend", "chatcompletions", "-temperature", "0"},
		{"-backend", "chatcompletions", "-thinking=false"},
		{"-backend", "chatcompletions", "-force-nonempty-content=false"},
		{"-temperature", "NaN"}, {"-temperature", "-1"}, {"-top-p", "0"},
		{"-max-tokens", "0"}, {"-top-k", "1.5"}, {"-thinking=maybe"},
	} {
		_, err := parseOptions(append([]string{"-config", "models.json"}, args...), io.Discard)
		if err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func valuePtr[T any](v T) *T { return &v }
