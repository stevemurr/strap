package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/harness"
)

func httpConfig(t *testing.T) harness.Config {
	t.Helper()
	c := harness.DefaultConfig()
	c.Dir, c.Web, c.LocalTools = t.TempDir(), nil, false
	return c
}

// HTTP mode refuses to start without a token or on an address it must not
// expose in the clear.
func TestRunHTTPRefusesUnsafeConfigurations(t *testing.T) {
	ctx := context.Background()
	cfg := httpConfig(t)
	for _, tc := range []struct {
		name, address, token, contains string
	}{
		{"no token", "127.0.0.1:0", "", "STRAP_API_TOKEN"},
		{"malformed address", "not-an-address", "token", "missing port"},
		{"hostname rather than an IP", "localhost:0", "token", "loopback"},
		{"non-loopback address", "0.0.0.0:0", "token", "loopback"},
	} {
		err := runHTTP(ctx, cfg, tc.address, tc.token)
		if err == nil {
			t.Fatal("started despite", tc.name)
		}
		if !strings.Contains(err.Error(), tc.contains) {
			t.Fatal(tc.name, err)
		}
	}
	// A port already in use is reported rather than silently retried.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := runHTTP(ctx, cfg, held.Addr().String(), "token"); err == nil {
		t.Fatal("bound a port that was already in use")
	}
}

// HTTP mode serves the harness API until its context is cancelled, and shuts
// down cleanly rather than reporting the shutdown as a failure.
func TestRunHTTPServesUntilCancelled(t *testing.T) {
	// Bind first to learn a free port, then release it for the server.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runHTTP(ctx, httpConfig(t), address, "token") }()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	var body string
	for {
		if time.Now().After(deadline) {
			t.Fatal("server never became reachable")
		}
		req, err := http.NewRequest("GET", "http://"+address+"/sessions", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer token")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body = string(raw)
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode, body)
		}
		break
	}
	if !strings.Contains(body, "[") {
		t.Fatal("session listing was not JSON", body)
	}
	// An unauthorized request is refused by the same listener.
	resp, err := client.Get("http://" + address + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("cancellation was reported as a failure:", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not stop when its context was cancelled")
	}
}

// The CLI routes -listen into HTTP mode, carrying the token requirement with it.
func TestRunDispatchesListenToHTTPMode(t *testing.T) {
	t.Setenv("STRAP_API_TOKEN", "")
	err := run(context.Background(), []string{"-listen", "127.0.0.1:0", "-C", t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "STRAP_API_TOKEN") {
		t.Fatal("the -listen flag did not enter HTTP mode", err)
	}
}
