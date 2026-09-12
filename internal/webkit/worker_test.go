package webkit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary doubles as a worker fixture; no Python or browser required.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "--worker" {
		fakeWorker()
		os.Exit(0)
	}
}

func fakeWorker() {
	var mu sync.Mutex
	jobs := map[string]chan struct{}{}
	write := func(v any) { mu.Lock(); defer mu.Unlock(); _ = json.NewEncoder(os.Stdout).Encode(v) }
	write(map[string]any{"type": "ready", "protocol": 1, "max_concurrency": 4})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 2<<20)
	for scanner.Scan() {
		var req struct{ ID, Method, URL string }
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if req.Method == "cancel" {
			mu.Lock()
			if ch := jobs[req.ID]; ch != nil {
				close(ch)
				delete(jobs, req.ID)
			}
			mu.Unlock()
			continue
		}
		if req.URL == "crash" {
			os.Exit(7)
		}
		if req.URL == "invalid" {
			fmt.Fprintln(os.Stdout, `{not-json}`)
			continue
		}
		cancel := make(chan struct{})
		mu.Lock()
		jobs[req.ID] = cancel
		mu.Unlock()
		go func(id, url string, cancel chan struct{}) {
			delay := time.Millisecond
			if url == "slow" {
				delay = 400 * time.Millisecond
			}
			if url == "hang" {
				delay = time.Minute
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				write(map[string]any{"id": id, "ok": true, "page": Page{URL: url, HTML: fmt.Sprint(os.Getpid())}})
			case <-cancel:
				write(map[string]any{"id": id, "ok": false, "code": "cancelled", "error": "cancelled"})
			}
			mu.Lock()
			delete(jobs, id)
			mu.Unlock()
		}(req.ID, req.URL, cancel)
	}
}

func testWorker(t *testing.T) *Worker {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := New(program)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return w
}

func TestWorkerMultiplexesReusesAndCancelsIndependently(t *testing.T) {
	w := testWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	warm, err := w.Search(ctx, "warm")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan result, 2)
	for _, url := range []string{"slow", "fast"} {
		go func(url string) { p, err := w.Search(ctx, url); finished <- result{page: p, err: err} }(url)
	}
	fast := <-finished
	if fast.err != nil || fast.page.URL != "fast" || fast.page.HTML != warm.HTML {
		t.Fatalf("not multiplexed: %+v", fast)
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, err := w.Search(short, "hang"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	slow := <-finished
	if slow.err != nil || slow.page.URL != "slow" || slow.page.HTML != warm.HTML {
		t.Fatalf("cancel affected another request: %+v", slow)
	}
	next, err := w.Search(ctx, "after-cancel")
	if err != nil || next.HTML != warm.HTML {
		t.Fatalf("cancel restarted worker: %+v %v", next, err)
	}
}

func TestWorkerRecoversFromCrashAndMalformedResponse(t *testing.T) {
	w := testWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, failure := range []string{"crash", "invalid"} {
		before, err := w.Search(ctx, "before")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Search(ctx, failure); err == nil {
			t.Fatal("accepted", failure)
		}
		after, err := w.Search(ctx, "after")
		if err != nil || before.HTML == after.HTML {
			t.Fatalf("did not restart: %v", err)
		}
	}
}

func TestWorkerCloseFailsOutstandingAndReaps(t *testing.T) {
	w := testWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.Search(ctx, "warm"); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	process := w.current
	w.mu.Unlock()
	completed := make(chan error, 1)
	go func() { _, err := w.Search(ctx, "hang"); completed <- err }()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err == nil {
		t.Fatal("outstanding request succeeded after close")
	}
	select {
	case <-process.done:
	default:
		t.Fatal("worker not reaped")
	}
	if _, err := w.Search(ctx, "after-close"); err == nil {
		t.Fatal("restarted closed worker")
	}
}

func TestWorkerRejectsOldProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-worker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"ready\",\"protocol\":0,\"max_concurrency\":4}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	w := New(path)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer w.Close(ctx)
	if _, err := w.Search(ctx, "test"); err == nil || !strings.Contains(err.Error(), "protocol 1") {
		t.Fatal(err)
	}
}
