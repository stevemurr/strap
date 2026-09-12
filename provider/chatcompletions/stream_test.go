package chatcompletions_test

import (
	"context"
	"fmt"
	"github.com/stevemurr/strap/provider"
	"github.com/stevemurr/strap/provider/chatcompletions"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeltaArrivesBeforeHTTPResponseFinishes(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	c, err := chatcompletions.New(chatcompletions.Config{BaseURL: server.URL, Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	delta, done := make(chan string, 1), make(chan error, 1)
	go func() {
		r, e := c.Submit(ctx, provider.Request{}, provider.ObserverFunc(func(d provider.Delta) error { delta <- d.Text; return nil }))
		if e == nil && r.Content != "hello" {
			e = fmt.Errorf("unexpected content %q", r.Content)
		}
		done <- e
	}()
	select {
	case text := <-delta:
		if text != "hello" {
			t.Fatal(text)
		}
	case <-ctx.Done():
		t.Fatal("delta was buffered until completion")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
