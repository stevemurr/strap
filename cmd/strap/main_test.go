package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpDoesNotOpenTerminalOrModel(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-base-url", "-model", "-timeout", "-C", "-backend", "-preset", "-temperature", "-thinking", "-web", "-wkrender", "-agent-browser", "-browser-executable"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatal(out.String())
		}
	}
}

func TestInvalidFlagsFailBeforeStartingConversation(t *testing.T) {
	for _, args := range [][]string{{"-timeout", "0s"}, {"-base-url", "not-a-url"}, {"extra"}, {"-C", filepath.Join(t.TempDir(), "missing")}, {"-temperature", "NaN"}, {"-backend", "unknown"}, {"-preset", "unknown"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
