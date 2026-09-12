package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stevemurr/strap/message"
	"github.com/stevemurr/strap/work"
)

func arithmeticServer(t *testing.T) *httptest.Server {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
			Tools    []struct{ Function struct{ Name string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		var env message.Message
		for _, m := range request.Messages {
			if m.Role == "user" {
				if err := json.Unmarshal([]byte(m.Content), &env); err != nil {
					t.Error(err)
				}
			}
		}
		name := ""
		var args any
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "user" {
			for _, op := range request.Tools {
				switch op.Function.Name {
				case "assign_work":
					if env.Event != nil && env.Event.Kind == work.ReviewRequested {
						item := env.Event.Work
						name = "assign_work"
						args = map[string]any{"kind": "audit", "work_id": item.ID, "expected_revision": item.Revision, "submission_id": item.LatestSubmissionID}
					} else if env.Kind == message.Instruction {
						name = "assign_work"
						args = map[string]any{"kind": "implementation", "task": "calculate two plus two"}
					}
				case "submit_work":
					if env.Work != nil {
						name = "submit_work"
						args = map[string]any{"work_id": env.Work.ID, "expected_revision": env.Work.Revision, "summary": "4"}
					}
				case "submit_audit":
					if env.Work != nil {
						name = "submit_audit"
						args = map[string]any{"work_id": env.Work.ID, "expected_revision": env.Work.Revision, "submission_id": env.Work.SubjectSubmissionID, "summary": "2 + 2 = 4", "verdict": "pass"}
					}
				}
			}
		}
		msg := map[string]any{"role": "assistant", "content": "Waiting for audited work."}
		reason := "stop"
		if name != "" {
			raw, _ := json.Marshal(args)
			reason = "tool_calls"
			msg["tool_calls"] = []any{map[string]any{"id": fmt.Sprint(calls.Add(1)), "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": reason, "message": msg}}})
	}))
	t.Cleanup(server.Close)
	return server
}
func TestLocalExampleAuditsThroughHTTP(t *testing.T) {
	server := arithmeticServer(t)
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	oldOutput := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = oldOutput }()
	oldFlags, oldArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine = oldFlags; os.Args = oldArgs }()
	flag.CommandLine = flag.NewFlagSet("local", flag.ContinueOnError)
	os.Args = []string{"local", "-base-url", server.URL, "-model", "test", "-timeout", "3s"}
	main()
	data, _ := os.ReadFile(output.Name())
	if !strings.Contains(string(data), "Verified audited delegation flow.") {
		t.Fatal(string(data))
	}
}
func TestLocalExampleErrors(t *testing.T) {
	if err := run(context.Background(), "invalid", "model"); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, "http://127.0.0.1:1", "model"); err == nil {
		t.Fatal("canceled run succeeded")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "model unavailable", 503) }))
	defer server.Close()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := run(ctx, server.URL, "model"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal(err)
	}
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer stalled.Close()
	defer close(release)
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := run(ctx, stalled.URL, "model"); err == nil {
		t.Fatal("deadline ignored")
	}
}

func TestLocalExampleMainFailureExit(t *testing.T) {
	oldFlags, oldArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine = oldFlags; os.Args = oldArgs }()
	flag.CommandLine = flag.NewFlagSet("local", flag.ContinueOnError)
	os.Args = []string{"local", "-base-url", "invalid"}
	code := 0
	mainWithExit(func(got int) { code = got })
	if code != 1 {
		t.Fatal(code)
	}
}
