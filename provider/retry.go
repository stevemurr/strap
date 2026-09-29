package provider

import (
	"context"
	"errors"
	"io"
	"syscall"
	"time"
)

// ErrEmptyResponse marks a completed response with neither text nor tool
// calls. The model server occasionally returns one; the same request made
// again usually succeeds.
var ErrEmptyResponse = errors.New("response contains no text or tool calls")

// Retryable reports whether a failed call may succeed if made again
// unchanged: an empty response, a rate limit or server error, or a dropped or
// refused connection. Cancellation, timeouts and rejected requests are not.
func Retryable(err error) bool {
	switch {
	case err == nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	case errors.Is(err, ErrEmptyResponse), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED):
		return true
	}
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) {
		s := status.HTTPStatus()
		return s == 429 || s >= 500
	}
	return false
}

// retryBackoff is the wait before each retry: 2s, 8s, then 32s.
var retryBackoff = func(attempt int) time.Duration { return 2 * time.Second << (2 * (attempt - 1)) }

type retrying struct {
	Provider
	attempts int
}

// WithRetries makes each call up to attempts more times while it fails with a
// retryable error, waiting between tries. One failed call used to end the
// agent that made it: a lone agent has no one to recover it (2026-09-28, an
// empty response). Every attempt streams to the same observer, so a recorded
// output holds them all and replays as the one call the agent saw.
func WithRetries(p Provider, attempts int) Provider { return retrying{Provider: p, attempts: attempts} }

func (p retrying) Submit(ctx context.Context, r Request, obs Observer) (Response, error) {
	resp, err := p.Provider.Submit(ctx, r, obs)
	for attempt := 1; attempt <= p.attempts && Retryable(err) && ctx.Err() == nil; attempt++ {
		select {
		case <-ctx.Done():
			return resp, err
		case <-time.After(retryBackoff(attempt)):
		}
		resp, err = p.Provider.Submit(ctx, r, obs)
	}
	return resp, err
}
