package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpDoesNotOpenTerminalOrModel(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-base-url", "-model", "-timeout", "-C", "-backend", "-temperature", "-thinking", "-web", "-wkrender", "-agent-browser", "-browser-executable"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatal(out.String())
		}
	}
}

// Option parsing rejects what it cannot act on, before anything is started.
func TestOptionsRejectUnusableInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	catalog := []string{"-config", catalogPath}
	http := []string{"-config", catalogPath, "-listen", "127.0.0.1:0"}
	for _, tc := range []struct{ prefix, args []string }{
		{nil, []string{"-timeout", "0s"}}, {nil, []string{"-timeout", "0"}}, {nil, []string{"-timeout", "-1s"}},
		{nil, []string{"-base-url", "not-a-url"}}, {nil, []string{"extra"}}, {nil, []string{"unexpected-positional"}},
		{nil, []string{"-C", filepath.Join(t.TempDir(), "missing")}}, {nil, []string{"-temperature", "NaN"}},
		{nil, []string{"-backend", "unknown"}}, {nil, []string{"-not-a-flag"}}, {nil, []string{"-unknown"}},
		{nil, []string{"-config", "/nonexistent/models.json"}},
		{http, []string{"-backend", "unknown"}}, {http, []string{"-temperature", "NaN"}}, {http, []string{"-base-url", "bad"}},
		{http, []string{"-model", ""}}, {http, []string{"-timeout", "0s"}}, {http, []string{"-C", catalogPath}}, {http, []string{"-profile", "missing"}},
		{catalog, []string{"-backend", "unknown"}},
		{catalog, []string{"-backend", "chatcompletions", "-temperature", "0"}},
		{catalog, []string{"-backend", "chatcompletions", "-thinking=false"}},
		{catalog, []string{"-backend", "chatcompletions", "-reasoning-effort", "low"}},
		{catalog, []string{"-reasoning-effort", "high"}}, {catalog, []string{"-reasoning-effort", ""}},
		{catalog, []string{"-backend", "chatcompletions", "-force-nonempty-content=false"}},
		{catalog, []string{"-temperature", "NaN"}}, {catalog, []string{"-temperature", "-1"}}, {catalog, []string{"-top-p", "0"}},
		{catalog, []string{"-max-tokens", "0"}}, {catalog, []string{"-top-k", "1.5"}}, {catalog, []string{"-thinking=maybe"}},
	} {
		args := append(append([]string(nil), tc.prefix...), tc.args...)
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		} else if errors.Is(err, context.Canceled) {
			t.Errorf("wrong error for %v: %v", args, err)
		}
	}
}
