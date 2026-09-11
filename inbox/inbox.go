// Package inbox provides a single-consumer, in-memory FIFO mailbox.
package inbox

import (
	"context"
	"errors"
	"sync"
)

var ErrClosed = errors.New("inbox closed")

// Inbox queues values independently of its consumer's execution. Its channel is
// only a wakeup signal, so a busy consumer never blocks a producer. The queue is
// unbounded; storage, retention, and backpressure are outside this first core.
// Construct with New. Do not copy an Inbox.
type Inbox[T any] struct {
	mu     sync.Mutex
	items  []T
	wake   chan struct{}
	closed bool
}

func New[T any]() *Inbox[T] {
	return &Inbox[T]{wake: make(chan struct{}, 1)}
}

func (q *Inbox[T]) Send(value T) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrClosed
	}
	q.items = append(q.items, value)
	q.signal()
	return nil
}

// Receive waits for one value. Closing preserves already queued values; after
// draining them, Receive returns ErrClosed. A cancelled context takes precedence.
func (q *Inbox[T]) Receive(ctx context.Context) (T, error) {
	for {
		var zero T
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		q.mu.Lock()
		if len(q.items) > 0 {
			value := q.items[0]
			q.items[0] = zero
			q.items = q.items[1:]
			if len(q.items) == 0 {
				q.items = nil
			}
			q.mu.Unlock()
			return value, nil
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return zero, ErrClosed
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-q.wake:
		}
	}
}

// Drain removes the messages currently queued without waiting for more.
func (q *Inbox[T]) Drain() []T {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}

func (q *Inbox[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.signal()
}

func (q *Inbox[T]) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Wait waits for available input without removing it. It shares the single
// consumer contract with Receive and lets a loop check controls before dequeueing.
func (q *Inbox[T]) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		available, closed := len(q.items) > 0, q.closed
		q.mu.Unlock()
		if available {
			return nil
		}
		if closed {
			return ErrClosed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-q.wake:
		}
	}
}
