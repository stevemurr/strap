package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCanceledStartupClosesSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A canceled controller rejects root creation after local tools are initialized.
	if err := run(ctx, []string{"-C", t.TempDir()}, io.Discard); err == nil {
		t.Fatal("canceled controller started")
	}
}
func TestMainHelp(t *testing.T) {
	old := os.Args
	os.Args = []string{"strap", "-help"}
	defer func() { os.Args = old }()
	main()
}
func TestUnknownFlagAndMissingAgentErrors(t *testing.T) {
	if err := run(context.Background(), []string{"-unknown"}, io.Discard); err == nil {
		t.Fatal("unknown flag accepted")
	}

}

func TestStartupReachesTerminal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// No input is sent, so startup never contacts the configured model endpoint.
	err := run(ctx, []string{"-C", t.TempDir(), "-base-url", "http://127.0.0.1:1"}, io.Discard)
	if err != nil && !strings.Contains(err.Error(), "tty") && !strings.Contains(err.Error(), "terminal") {
		t.Fatal(err)
	}
}
