package chatwire

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/stevemurr/strap/provider"
)

// StallPolicy bounds how long a streaming response may deliver nothing. The
// two budgets differ because the waits differ in kind: before the first chunk
// the server is reading the prompt, which grows with context and queueing,
// while a stream that has started should never pause for long. Recorded
// generation delivers a chunk roughly every two tokens and has never paused
// beyond ~2s, so a stream that goes quiet for tens of seconds is producing
// tokens that will never arrive rather than thinking.
type StallPolicy struct {
	FirstChunk time.Duration // Zero disables the wait for the first chunk.
	Idle       time.Duration // Zero disables the wait between chunks.
}

func (p StallPolicy) enabled() bool { return p.FirstChunk > 0 || p.Idle > 0 }

// watchStall cancels the request when the body stops delivering. Read blocks
// for the whole silence, so the reader cannot notice its own stall; a separate
// ticker compares the last delivery against the budget in force. Cancelling the
// request unblocks that Read, which is what surfaces the stall as an error.
func watchStall(ctx context.Context, body io.Reader, policy StallPolicy, cancel context.CancelCauseFunc) (io.Reader, func()) {
	if !policy.enabled() {
		return body, func() {}
	}
	r := &stallReader{body: body}
	r.last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	go func() {
		// A coarse tick is enough: the budgets are seconds and the signal is
		// tens of seconds of silence, so the extra precision of resetting a
		// timer on every chunk would buy nothing.
		interval := min(policy.FirstChunk, policy.Idle)
		if interval <= 0 {
			interval = max(policy.FirstChunk, policy.Idle)
		}
		interval = max(interval/4, 250*time.Millisecond)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				budget := policy.FirstChunk
				if r.delivered.Load() {
					budget = policy.Idle
				}
				if budget <= 0 {
					continue
				}
				quiet := time.Since(time.Unix(0, r.last.Load()))
				if quiet < budget {
					continue
				}
				cancel(&StallError{Quiet: quiet, Budget: budget, Delivered: r.delivered.Load()})
				return
			}
		}
	}()
	return r, func() { close(done) }
}

type stallReader struct {
	body      io.Reader
	last      atomic.Int64
	delivered atomic.Bool
}

func (r *stallReader) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		r.last.Store(time.Now().UnixNano())
		r.delivered.Store(true)
	}
	return n, err
}

// StallError reports a response abandoned for delivering nothing. It wraps
// provider.ErrStreamStalled so a caller can retry the request without having to
// match on transport detail: nothing was committed, so the attempt simply did
// not happen.
type StallError struct {
	Quiet     time.Duration
	Budget    time.Duration
	Delivered bool
}

func (e *StallError) Error() string {
	stage := "before its first token"
	if e.Delivered {
		stage = "mid-stream"
	}
	quiet := e.Quiet.Round(time.Second)
	if e.Quiet < 10*time.Second {
		quiet = e.Quiet.Round(time.Millisecond)
	}
	return fmt.Sprintf("model stream stalled %s: nothing arrived for %s (budget %s)", stage, quiet, e.Budget)
}

func (e *StallError) Unwrap() error { return provider.ErrStreamStalled }
