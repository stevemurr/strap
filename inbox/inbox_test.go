package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/inbox"
)

func TestFIFOAndCloseDrain(t *testing.T) {
	q := inbox.New[int]()
	for i := range 1000 {
		if err := q.Send(i); err != nil {
			t.Fatal(err)
		}
	}
	q.Close()
	for i := range 1000 {
		got, err := q.Receive(context.Background())
		if err != nil || got != i {
			t.Fatalf("receive %d: %d, %v", i, got, err)
		}
	}
	if _, err := q.Receive(context.Background()); !errors.Is(err, inbox.ErrClosed) {
		t.Fatal(err)
	}
	if err := q.Send(1001); !errors.Is(err, inbox.ErrClosed) {
		t.Fatal(err)
	}
}

func TestCloseWakesWaitingConsumer(t *testing.T) {
	q := inbox.New[int]()
	done := make(chan error, 1)
	go func() { _, err := q.Receive(context.Background()); done <- err }()
	q.Close()
	select {
	case err := <-done:
		if !errors.Is(err, inbox.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("receive did not wake")
	}
}

func TestCancelledReceiveDoesNotConsume(t *testing.T) {
	q := inbox.New[int]()
	_ = q.Send(42)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	value, err := q.Receive(context.Background())
	if err != nil || value != 42 {
		t.Fatalf("%d, %v", value, err)
	}
}

func TestSendAndCloseRacePreservesEveryAcceptedValue(t *testing.T) {
	for range 100 {
		q := inbox.New[int]()
		accepted := make(chan bool, 1)
		go func() { accepted <- q.Send(7) == nil }()
		q.Close()
		ok := <-accepted
		value, err := q.Receive(context.Background())
		if ok && (err != nil || value != 7) {
			t.Fatalf("lost accepted value: %d %v", value, err)
		}
		if !ok && !errors.Is(err, inbox.ErrClosed) {
			t.Fatalf("unexpected value: %d %v", value, err)
		}
	}
}
