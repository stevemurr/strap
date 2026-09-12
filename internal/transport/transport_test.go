package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPoolsAreIndependentAndReuseWithinSession(t *testing.T) {
	addresses := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addresses <- r.RemoteAddr
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	a, b := New(), New()
	t.Cleanup(func() { _ = a.Close(context.Background()); _ = b.Close(context.Background()) })
	get := func(pool *Transport) string {
		t.Helper()
		response, err := (&http.Client{Transport: pool}).Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadAll(response.Body); err != nil {
			t.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		return <-addresses
	}
	first := get(a)
	if next := get(a); next != first {
		t.Fatalf("connection not reused: %s, %s", first, next)
	}
	other := get(b)
	if other == first {
		t.Fatal("sessions shared a connection")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if next := get(b); next != other {
		t.Fatal("closing sibling disrupted pool")
	}
	_, err := (&http.Client{Transport: a}).Get(server.URL)
	if !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestCloseCancelsUnreadResponseBody(t *testing.T) {
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	pool := New()
	response, err := (&http.Client{Transport: pool}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("active response was not cancelled")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRequestCancellationReleasesResource(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	pool := New()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	result := make(chan error, 1)
	go func() { _, err := (&http.Client{Transport: pool}).Do(request); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cleanup, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := pool.Close(cleanup); err != nil {
		t.Fatal(err)
	}
}
