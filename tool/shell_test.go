//go:build darwin || linux

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellTool(t *testing.T, config ShellConfig) *Shell {
	t.Helper()
	s, err := NewShell(config)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestShellOutputExitAndWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	s := shellTool(t, ShellConfig{Dir: dir})
	var result ShellResult
	callJSON(t, s, map[string]any{"command": "printf out; printf err >&2; printf contents > file; exit 7"}, &result)
	if result.Output != "outerr" || result.ExitCode == nil || *result.ExitCode != 7 || result.TimedOut || result.Truncated {
		t.Fatalf("command result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || string(data) != "contents" {
		t.Fatalf("working directory: %q, %v", data, err)
	}
}

func TestShellBoundsOutputWhileDraining(t *testing.T) {
	s := shellTool(t, ShellConfig{Dir: t.TempDir(), OutputLimit: 16})
	var result ShellResult
	callJSON(t, s, map[string]any{"command": "printf START___; i=0; while [ $i -lt 5000 ]; do printf 0123456789; i=$((i+1)); done; printf ____END"}, &result)
	if !result.Truncated || !strings.HasPrefix(result.Output, "START___") || !strings.HasSuffix(result.Output, "____END") || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("bounded result: %+v", result)
	}
	if len(result.Output) > 16+len("\n[output truncated]\n") {
		t.Fatalf("output escaped cap: %d", len(result.Output))
	}
}

func TestBoundedOutputChunkBoundaries(t *testing.T) {
	full := strings.Repeat("abcdefghi", 20)
	for limit := 2; limit <= len(full)+1; limit++ {
		for chunk := 1; chunk <= 25; chunk++ {
			out := newBoundedOutput(limit)
			for start := 0; start < len(full); start += chunk {
				part := full[start:min(start+chunk, len(full))]
				if n, err := out.Write([]byte(part)); n != len(part) || err != nil {
					t.Fatalf("write: %d, %v", n, err)
				}
			}
			want := full
			if len(full) > limit {
				want = full[:(limit+1)/2] + "\n[output truncated]\n" + full[len(full)-limit/2:]
			}
			if got := out.String(); got != want || out.truncated != (len(full) > limit) {
				t.Fatalf("limit %d chunk %d: %q != %q", limit, chunk, got, want)
			}
		}
	}
}

func TestShellTimeoutAndDescendantCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := shellTool(t, ShellConfig{Dir: dir})
	var result ShellResult
	start := time.Now()
	callJSON(t, s, map[string]any{"command": "printf started; (sleep 1; printf leaked > marker) & wait", "timeout_ms": 50}, &result)
	if !result.TimedOut || result.Output != "started" || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout result: %+v", result)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived timeout: %v", err)
	}
}

func TestShellCleansDescendantsAfterNormalExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := shellTool(t, ShellConfig{Dir: dir})
	var result ShellResult
	callJSON(t, s, map[string]any{"command": "(sleep 1; printf leaked > marker) & printf done"}, &result)
	if result.Output != "done" || result.ExitCode == nil || *result.ExitCode != 0 || !result.OutputIncomplete {
		t.Fatalf("exit with open descendant pipe: %+v", result)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived normal exit: %v", err)
	}
}

func TestShellCancellation(t *testing.T) {
	s := shellTool(t, ShellConfig{Dir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	_, err := s.Call(ctx, Call{Arguments: json.RawMessage(`{"command":"sleep 10"}`)})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel: %v after %s", err, time.Since(start))
	}
}

func TestShellEnvironmentAndArgumentValidation(t *testing.T) {
	t.Setenv("STRAP_TEST_PRIVATE", "should-not-inherit")
	s := shellTool(t, ShellConfig{Dir: t.TempDir()})
	var result ShellResult
	callJSON(t, s, map[string]any{"command": "printf '%s' \"${STRAP_TEST_PRIVATE-unset}\""}, &result)
	if result.Output != "unset" {
		t.Fatalf("inherited private environment: %q", result.Output)
	}
	env := []string{"STRAP_TEST_VALUE=original"}
	s = shellTool(t, ShellConfig{Dir: t.TempDir(), Env: env})
	env[0] = "STRAP_TEST_VALUE=changed"
	callJSON(t, s, map[string]any{"command": "printf '%s' \"$STRAP_TEST_VALUE\""}, &result)
	if result.Output != "original" {
		t.Fatalf("configuration was not copied: %q", result.Output)
	}
	for _, raw := range []string{
		`{}`, `{"command":""}`, `{"command":"  "}`, `{"command":null}`,
		`{"command":"true","timeout_ms":0}`, `{"command":"true","timeout_ms":-1}`,
		`{"command":"true","timeout_ms":300001}`, `{"command":"true","timeout_ms":9223372036854775807}`,
		`{"command":"true","timeout_ms":null}`, `{"command":"true","background":true}`,
	} {
		if _, err := s.Call(context.Background(), Call{Arguments: json.RawMessage(raw)}); err == nil {
			t.Errorf("accepted invalid arguments: %s", raw)
		}
	}
}

func TestShellStartFailure(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "shell")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s := shellTool(t, ShellConfig{Dir: dir, Program: program})
	if err := os.Remove(program); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(context.Background(), Call{Arguments: json.RawMessage(`{"command":"true"}`)}); err == nil {
		t.Fatal("start failure was reported as a command result")
	}
}

func TestShellCancellationKeepsPartialOutputAndCleanupFailure(t *testing.T) {
	s := shellTool(t, ShellConfig{Dir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	raw, err := s.Call(ctx, Call{Arguments: json.RawMessage(`{"command":"printf partial-evidence; sleep 10"}`)})
	var result ShellResult
	if e := json.Unmarshal([]byte(raw.Content.Text()), &result); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(err, context.Canceled) || !result.Cancelled || !result.Started || result.Output != "partial-evidence" || result.ExitCode != nil || result.OutputLimit != 64*1024 {
		t.Fatal(result, err)
	}
	s.stop = func(cmd *exec.Cmd) error { _ = stopProcessGroup(cmd); return errors.New("cleanup rejected") }
	raw, err = s.Call(context.Background(), Call{Arguments: json.RawMessage(`{"command":"printf retained"}`)})
	if e := json.Unmarshal([]byte(raw.Content.Text()), &result); e != nil {
		t.Fatal(e)
	}
	if err == nil || result.CleanupError != "cleanup rejected" || result.Output != "retained" || result.ExitCode == nil {
		t.Fatal(result, err)
	}
}
