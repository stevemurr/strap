package main

import (
	"context"
	"encoding/json"
	"flag"
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
		"presence_penalty": 0.0, "repetition_penalty": 1.0, "max_tokens": 32768.0,
		"chat_template_kwargs": map[string]any{"enable_thinking": true},
	}
	for _, tc := range []struct {
		name    string
		args    []string
		want    map[string]any
		generic bool
	}{
		{"default", nil, preset, false},
		{"explicit preset", []string{"-preset", "qwen3.6-coding"}, preset, false},
		{"server defaults", []string{"-preset", "none"}, map[string]any{}, false},
		{"zero and false overrides", []string{"-temperature", "0", "-thinking=false", "-max-tokens", "4096"}, map[string]any{
			"temperature": 0.0, "top_p": 0.95, "top_k": 20.0, "min_p": 0.0, "presence_penalty": 0.0,
			"repetition_penalty": 1.0, "max_tokens": 4096.0, "chat_template_kwargs": map[string]any{"enable_thinking": false},
		}, false},
		{"overrides without preset", []string{"-preset", "none", "-top-p", "0.8", "-top-k", "-1", "-min-p", "0", "-presence-penalty", "0", "-repetition-penalty", "1.1", "-thinking"}, map[string]any{
			"top_p": 0.8, "top_k": -1.0, "min_p": 0.0, "presence_penalty": 0.0, "repetition_penalty": 1.1,
			"chat_template_kwargs": map[string]any{"enable_thinking": true},
		}, false},
		{"generic", []string{"-backend", "chatcompletions"}, map[string]any{}, true},
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
				if !reflect.DeepEqual(body, tc.want) {
					t.Errorf("got %#v, want %#v", body, tc.want)
				}
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			o := modelFlags(flags)
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			p, err := o.config(server.URL, "arbitrary-server-alias").NewProvider(server.Client())
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
			if _, err := p.Submit(context.Background(), provider.Request{}); err != nil {
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
		{"-temperature", "NaN"}, {"-temperature", "-1"}, {"-top-p", "0"},
		{"-max-tokens", "0"}, {"-top-k", "1.5"}, {"-thinking=maybe"},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		o := modelFlags(flags)
		err := flags.Parse(args)
		if err == nil {
			_, err = o.config("http://127.0.0.1:1", "local").NewProvider(nil)
		}
		if err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func valuePtr[T any](v T) *T { return &v }
