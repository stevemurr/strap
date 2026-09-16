package main

import (
	"bytes"
	"context"
	"github.com/stevemurr/strap/harness"
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

func TestInvalidFlagsFailBeforeStartingConversation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"-timeout", "0s"}, {"-base-url", "not-a-url"}, {"extra"}, {"-C", filepath.Join(t.TempDir(), "missing")}, {"-temperature", "NaN"}, {"-backend", "unknown"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestHTTPModeRequiresTokenAndLoopbackAddress(t *testing.T) {
	if err := runHTTP(context.Background(), harness.DefaultConfig(), "127.0.0.1:0", ""); err == nil {
		t.Fatal("missing token accepted")
	}
	if err := runHTTP(context.Background(), harness.DefaultConfig(), "0.0.0.0:0", "test"); err == nil {
		t.Fatal("non-loopback CLI server accepted")
	}
}
