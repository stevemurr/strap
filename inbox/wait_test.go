package inbox_test

import (
	"context"
	"errors"
	"github.com/stevemurr/strap/inbox"
	"reflect"
	"testing"
	"time"
)

func TestWaitPreservesQueuedValuesAndDrainOrder(t *testing.T) {
	q := inbox.New[int]()
	done := make(chan error, 1)
	go func() { done <- q.Wait(context.Background()) }()
	if err := q.Send(7); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not wake")
	}
	q.Send(8)
	q.Close()
	if err := q.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := q.Drain(); !reflect.DeepEqual(got, []int{7, 8}) {
		t.Fatalf("drain = %v", got)
	}
	if got := q.Drain(); len(got) != 0 {
		t.Fatal(got)
	}
	if err := q.Wait(context.Background()); !errors.Is(err, inbox.ErrClosed) {
		t.Fatal(err)
	}
}

func TestBlockedReadersWakeOnCancelAndClose(t *testing.T) {
	for _, method := range []string{"wait", "receive"} {
		for _, end := range []string{"cancel", "close"} {
			t.Run(method+"/"+end, func(t *testing.T) {
				q := inbox.New[int]()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if method == "wait" {
						done <- q.Wait(ctx)
					} else {
						_, err := q.Receive(ctx)
						done <- err
					}
				}()
				// Give the reader an opportunity to block on an empty queue.
				time.Sleep(time.Millisecond)
				want := inbox.ErrClosed
				if end == "cancel" {
					cancel()
					want = context.Canceled
				} else {
					q.Close()
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("got %v, want %v", err, want)
					}
				case <-time.After(time.Second):
					t.Fatal("reader blocked")
				}
			})
		}
	}
}
func TestCanceledWaitPrecedesAvailableInput(t *testing.T) {
	q := inbox.New[int]()
	q.Send(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := q.Drain(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatal(got)
	}
}
