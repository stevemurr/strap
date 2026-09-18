package chatwire_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/vllm"
)

// A runaway generation keeps the GPU busy and sends nothing, so the watchdog
// cannot ask whether work is happening; it asks whether anything arrived.
func TestStalledStreamIsAbandonedAndNamed(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"thinking\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release // The server is working hard and saying nothing.
	}))
	defer server.Close()
	defer close(release)
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m",
		Stall: vllm.StallPolicy{FirstChunk: time.Minute, Idle: 300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = client.Submit(context.Background(), provider.Request{}, nil)
	if !errors.Is(err, provider.ErrStreamStalled) {
		t.Fatalf("stall not reported as itself: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("waited %s for a 300ms idle budget", took)
	}
	if !errors.Is(err, provider.ErrStreamStalled) || err.Error() == "" {
		t.Fatalf("unusable stall error: %v", err)
	}
	t.Logf("reported: %v", err)
}

// Reading the prompt is not a stall, so the first chunk gets its own budget:
// prefill grows with context while a started stream should not pause.
func TestSlowFirstChunkIsNotAStall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(400 * time.Millisecond) // Longer than the idle budget.
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m",
		Stall: vllm.StallPolicy{FirstChunk: 10 * time.Second, Idle: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Submit(context.Background(), provider.Request{}, nil)
	if err != nil || response.Content != "answer" {
		t.Fatalf("slow prefill treated as a stall: %v %+v", err, response)
	}
}

// An unconfigured policy must leave the transport exactly as it was.
func TestStallWatchdogIsOptional(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client, err := vllm.New(vllm.Config{BaseURL: server.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := client.Submit(context.Background(), provider.Request{}, nil); err != nil || response.Content != "ok" {
		t.Fatalf("unconfigured watchdog interfered: %v %+v", err, response)
	}
}
