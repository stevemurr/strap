package webkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stevemurr/strap/internal/webprocess"
)

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(b []byte) (int, error) { return f(b) }
func (writeFunc) Close() error                  { return nil }

func inertProcess() *process {
	return &process{cmd: exec.Command("/missing/worker"), stdin: writeFunc(func(b []byte) (int, error) { return len(b), nil }), stdout: io.NopCloser(strings.NewReader("")), write: make(chan struct{}, 1), ready: make(chan error, 1), done: make(chan struct{}), pending: make(map[string]chan result)}
}

func TestSendRejectsInvalidOrUnavailableRequests(t *testing.T) {
	for _, mode := range []string{"cancelled", "marshal", "oversized", "queued", "stopped", "failed", "write failure", "blocked write"} {
		t.Run(mode, func(t *testing.T) {
			w := testWorker(t)
			p := inertProcess()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var packet any = map[string]string{"method": "render"}
			want := ""
			sentinel := errors.New("broken worker")
			switch mode {
			case "cancelled":
				cancel()
				want = "context canceled"
			case "marshal":
				packet = make(chan int)
				want = "unsupported type"
			case "oversized":
				packet = strings.Repeat("x", 1<<20)
				want = "exceeds 1 MiB"
			case "queued":
				p.write <- struct{}{}
				want = "deadline exceeded"
			case "stopped":
				p.write <- struct{}{}
				close(p.done)
				want = "stopped"
			case "failed":
				p.err = sentinel
				want = sentinel.Error()
			case "write failure":
				p.stdin = writeFunc(func([]byte) (int, error) { return 0, sentinel })
				want = sentinel.Error()
			case "blocked write":
				reader, writer := io.Pipe()
				defer reader.Close()
				p.stdin = writer
				want = "deadline exceeded"
			}
			err := w.send(ctx, p, packet)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
			if mode == "write failure" || mode == "blocked write" {
				if p.err == nil {
					t.Fatal("failed writer left worker reusable")
				}
			}
		})
	}
}

func TestWorkerStartFailures(t *testing.T) {
	for _, mode := range []string{"queued cancellation", "queued close", "closed", "stdout pipe", "stdin pipe", "missing program"} {
		t.Run(mode, func(t *testing.T) {
			w := testWorker(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			want := ""
			switch mode {
			case "queued cancellation":
				w.startGate <- struct{}{}
				want = "deadline exceeded"
			case "queued close":
				w.startGate <- struct{}{}
				w.cancel()
				want = "closed"
			case "closed":
				w.closed = true
				want = "closed"
			case "stdout pipe":
				w.command = func(string, ...string) *exec.Cmd { cmd := exec.Command("unused"); cmd.Stdout = io.Discard; return cmd }
				want = "Stdout already set"
			case "stdin pipe":
				w.command = func(string, ...string) *exec.Cmd {
					cmd := exec.Command("unused")
					cmd.Stdin = strings.NewReader("")
					return cmd
				}
				want = "Stdin already set"
			case "missing program":
				w.program = "/missing/wkrender"
				want = "start wkrender"
			}
			if _, err := w.start(ctx); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}

func waitForCurrent(t *testing.T, w *Worker) *process {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		w.mu.Lock()
		p := w.current
		w.mu.Unlock()
		if p != nil {
			return p
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatal("worker did not start")
		}
	}
}

func TestHandshakeFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing", "invalid", "context", "close"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker")
			body := "#!/bin/sh\nsleep 30\n"
			want := ""
			switch mode {
			case "missing":
				body = "#!/bin/sh\nprintf 'startup diagnostic' >&2\n"
				// stderr is drained independently from stdout; its diagnostic
				// may not yet be buffered when the missing handshake is noticed.
				want = "did not send handshake"
			case "invalid":
				body = "#!/bin/sh\nprintf 'not-json\\n'\n"
				want = "protocol 1"
			case "context":
				want = "context canceled"
			case "close":
				want = "closed"
			}
			if err := os.WriteFile(path, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			w := testWorker(t)
			w.program = path
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := w.start(ctx); done <- err }()
			if mode == "context" || mode == "close" {
				waitForCurrent(t, w)
				if mode == "context" {
					cancel()
				} else {
					w.cancel()
				}
			}
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("want %q, got %v", want, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("handshake did not settle")
			}
		})
	}
}

func TestReadReportsStreamFailureAndIgnoresLateResponses(t *testing.T) {
	w := testWorker(t)
	p := inertProcess()
	w.current = p
	ch := make(chan result, 1)
	p.pending["waiting"] = ch
	w.wg.Add(1)
	source := io.MultiReader(strings.NewReader("{\"type\":\"ready\",\"protocol\":1,\"max_concurrency\":4}\n{\"id\":\"late\",\"ok\":true,\"page\":{}}\n"), iotest.ErrReader(errors.New("stream failed")))
	w.read(p, source, &webprocess.Buffer{Limit: 100})
	if err := <-p.ready; err != nil {
		t.Fatal(err)
	}
	if r := <-ch; r.err == nil || !strings.Contains(r.err.Error(), "read wkrender response: stream failed") {
		t.Fatal(r)
	}
	if w.current != nil || len(p.pending) != 0 {
		t.Fatal("failed stream remained active")
	}
	select {
	case <-p.done:
	default:
		t.Fatal("reader did not finish")
	}
}

func TestMissingHandshakeIncludesBufferedDiagnostic(t *testing.T) {
	w := testWorker(t)
	p := inertProcess()
	w.current = p
	stderr := &webprocess.Buffer{Limit: 100}
	stderr.Write([]byte("startup diagnostic"))
	w.wg.Add(1)
	w.read(p, strings.NewReader(""), stderr)
	if err := <-p.ready; err == nil || !strings.Contains(err.Error(), "handshake: startup diagnostic") {
		t.Fatal(err)
	}
}

func TestSearchQueueAndPreflightFailures(t *testing.T) {
	for _, mode := range []string{"cancelled", "queued cancellation", "queued close", "failed process", "send failure"} {
		t.Run(mode, func(t *testing.T) {
			w := testWorker(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			url := "https://example.com"
			want := ""
			switch mode {
			case "cancelled":
				cancel()
				want = "context canceled"
			case "queued cancellation", "queued close":
				for range cap(w.slots) {
					w.slots <- struct{}{}
				}
				want = "deadline exceeded"
				if mode == "queued close" {
					w.cancel()
					want = "closed"
				}
			case "failed process":
				w.current = inertProcess()
				w.current.err = errors.New("worker failed before dispatch")
				want = "worker failed before dispatch"
			case "send failure":
				w.current = inertProcess()
				url = strings.Repeat("x", 1<<20)
				want = "exceeds 1 MiB"
			}
			if _, err := w.Search(ctx, url); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
			if w.current != nil && len(w.current.pending) != 0 {
				t.Fatal("failed request left pending entry")
			}
		})
	}
}

func TestSearchDefaultDeadlineAndCancellationFailures(t *testing.T) {
	for _, mode := range []string{"success", "cancel write fails", "cancel unacknowledged"} {
		t.Run(mode, func(t *testing.T) {
			w := testWorker(t)
			p := inertProcess()
			w.current = p
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var methods []string
			rendered := make(chan struct{})
			p.stdin = writeFunc(func(b []byte) (int, error) {
				var packet struct {
					ID, Method, URL, Readiness string
					Timeout                    float64
				}
				if err := json.Unmarshal(b, &packet); err != nil {
					return 0, err
				}
				methods = append(methods, packet.Method)
				if packet.Method == "render" {
					if packet.Timeout <= 0 || packet.Timeout > 20 || packet.Readiness != "search" || packet.URL != "https://example.com" {
						t.Error("invalid render request", packet)
					}
					if mode == "success" {
						w.mu.Lock()
						ch := p.pending[packet.ID]
						delete(p.pending, packet.ID)
						w.mu.Unlock()
						ch <- result{page: Page{URL: packet.URL, HTML: "rendered"}}
					}
					close(rendered)
				} else if mode == "cancel write fails" {
					return 0, errors.New("cancel pipe broke")
				}
				return len(b), nil
			})
			finished := make(chan result, 1)
			go func() { page, err := w.Search(ctx, "https://example.com"); finished <- result{page: page, err: err} }()
			select {
			case <-rendered:
			case <-time.After(2 * time.Second):
				t.Fatal("render was not sent")
			}
			if mode != "success" {
				// Cancel after send has released its write lock, so this tests
				// cancellation of navigation rather than cancellation of a write.
				deadline := time.Now().Add(2 * time.Second)
				for len(p.write) != 0 {
					if time.Now().After(deadline) {
						t.Fatal("render write did not finish")
					}
					time.Sleep(time.Millisecond)
				}
				cancel()
			}
			var got result
			select {
			case got = <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("search did not settle")
			}
			page, err := got.page, got.err
			if mode == "success" {
				if err != nil || page.HTML != "rendered" || strings.Join(methods, ",") != "render" {
					t.Fatal(page, err, methods)
				}
			} else {
				if !errors.Is(err, context.Canceled) || strings.Join(methods, ",") != "render,cancel" || p.err == nil || len(p.pending) != 0 {
					t.Fatal(err, methods, p.err)
				}
				if mode == "cancel unacknowledged" && !strings.Contains(p.err.Error(), "did not acknowledge") {
					t.Fatal(p.err)
				}
			}
		})
	}
}

func TestCloseDeadlineWhileWorkerDrains(t *testing.T) {
	w := testWorker(t)
	w.wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.Close(ctx)
	w.wg.Done()
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
