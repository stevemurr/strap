package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"
)

type flaky struct {
	errs  []error
	calls int
}

func (f *flaky) Submit(context.Context, Request, Observer) (Response, error) {
	f.calls++
	if f.calls <= len(f.errs) {
		return Response{}, f.errs[f.calls-1]
	}
	return Response{Content: "ok"}, nil
}

type status int

func (s status) Error() string   { return fmt.Sprintf("HTTP %d", int(s)) }
func (s status) HTTPStatus() int { return int(s) }

func TestWithRetriesRepeatsOnlyRetryableFailures(t *testing.T) {
	retryBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { retryBackoff = func(a int) time.Duration { return 2 * time.Second << (2 * (a - 1)) } })
	// Two empty responses, then an answer.
	f := &flaky{errs: []error{ErrEmptyResponse, fmt.Errorf("vllm: %w", ErrEmptyResponse)}}
	if r, err := WithRetries(f, 3).Submit(context.Background(), Request{}, nil); err != nil || r.Content != "ok" || f.calls != 3 {
		t.Fatal(r, err, f.calls)
	}
	// A rejected request fails at once.
	f = &flaky{errs: []error{status(400)}}
	if _, err := WithRetries(f, 3).Submit(context.Background(), Request{}, nil); err == nil || f.calls != 1 {
		t.Fatal(err, f.calls)
	}
	// Retries run out.
	f = &flaky{errs: []error{status(502), status(502), status(502), status(502), status(502)}}
	if _, err := WithRetries(f, 3).Submit(context.Background(), Request{}, nil); err == nil || f.calls != 4 {
		t.Fatal(err, f.calls)
	}
	// Cancellation ends them.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = &flaky{errs: []error{ErrEmptyResponse}}
	if _, err := WithRetries(f, 3).Submit(ctx, Request{}, nil); err == nil || f.calls != 1 {
		t.Fatal(err, f.calls)
	}
}

func TestRetryableClassifiesFailures(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://model", Err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	for err, want := range map[error]bool{
		ErrEmptyResponse:                      true,
		fmt.Errorf("x: %w", ErrEmptyResponse): true,
		status(429):                           true,
		status(503):                           true,
		status(400):                           false,
		refused:                               true,
		context.Canceled:                      false,
		context.DeadlineExceeded:              false,
		errors.New("tool arguments invalid"):  false,
	} {
		if Retryable(err) != want {
			t.Errorf("Retryable(%v) = %v, want %v", err, !want, want)
		}
	}
}
