package webprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunBoundsOutputAndSeparatesDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := Run(ctx, "/bin/sh", []string{"-c", `printf '{"ok":true}'; printf 'diagnostic' >&2`}, "", 100)
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := Run(ctx, "/bin/sh", []string{"-c", `printf '01234567890123456789'`}, "", 10); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatal(err)
	}
	if _, err := Run(ctx, "/bin/sh", []string{"-c", `printf 'backend failed' >&2; exit 2`}, "", 100); err == nil || !strings.Contains(err.Error(), "backend failed") {
		t.Fatal(err)
	}
}

func TestRunStartFailureAndStdoutDiagnostic(t *testing.T) {
	if _, err := Run(context.Background(), "/missing/backend", nil, "", 100); err == nil || !strings.Contains(err.Error(), "start /missing/backend") {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), "/bin/sh", []string{"-c", "printf 'json failure'; exit 3"}, "", 100)
	if err == nil || !strings.Contains(err.Error(), "json failure") {
		t.Fatal(err)
	}
	if err := Kill(&exec.Cmd{}); err != nil {
		t.Fatal(err)
	}
}

func TestRunCancellationDrainsInheritedPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Run(ctx, "/bin/sh", []string{"-c", "sleep 30 & wait"}, "", 100)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("cancel did not stop/drain process group: %v", err)
	}
}

func TestBrowserEnvironmentDoesNotInheritAmbientSession(t *testing.T) {
	t.Setenv("AGENT_BROWSER_PROFILE", "User profile")
	t.Setenv("AGENT_BROWSER_CDP", "http://localhost:9222")
	t.Setenv("API_SECRET", "not-for-the-browser")
	for _, value := range Env() {
		if strings.HasPrefix(value, "AGENT_BROWSER_") || strings.HasPrefix(value, "API_SECRET=") {
			t.Fatal("ambient config inherited")
		}
	}
	if os.Getenv("HOME") != "" && len(Env()) == 0 {
		t.Fatal("host environment missing")
	}
}
