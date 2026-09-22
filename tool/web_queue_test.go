package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stevemurr/strap/internal/agentbrowser"
)

func TestOpenReadBudgetStartsAfterSlot(t *testing.T) {
	w := testWeb(t, WebConfig{OpenConcurrency: 1, OpenTimeout: 50 * time.Millisecond, OpenQueueTimeout: time.Second})
	w.openSlots <- struct{}{}
	entered := make(chan time.Duration, 1)
	w.browser = pageFunc(func(ctx context.Context, url string) (agentbrowser.Page, error) {
		deadline, _ := ctx.Deadline()
		entered <- time.Until(deadline)
		return agentbrowser.Page{URL: url, Content: "retained"}, nil
	})
	finished := make(chan error, 1)
	go func() {
		_, err := w.OpenPage(context.Background(), "a", "https://example.test", "", 200)
		finished <- err
	}()
	// Waiting longer than the read allowance must not consume that allowance.
	time.Sleep(100 * time.Millisecond)
	<-w.openSlots
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if left := <-entered; left < 25*time.Millisecond {
		t.Fatalf("read inherited queue time: %s", left)
	}
}

func TestOpenQueueTimeoutAndCloseReleaseWaiters(t *testing.T) {
	for _, closeRuntime := range []bool{false, true} {
		w := testWeb(t, WebConfig{OpenConcurrency: 1, OpenQueueTimeout: 20 * time.Millisecond})
		w.openSlots <- struct{}{}
		w.browser = pageFunc(func(context.Context, string) (agentbrowser.Page, error) {
			t.Error("queued call reached browser")
			return agentbrowser.Page{}, nil
		})
		finished := make(chan error, 1)
		go func() {
			_, err := w.OpenPage(context.Background(), "a", "https://example.test", "", 200)
			finished <- err
		}()
		if closeRuntime {
			if err := w.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		err := <-finished
		if closeRuntime {
			if err == nil {
				t.Fatal("closed runtime admitted read")
			}
		} else if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "queue") {
			t.Fatal(err)
		}
		if len(w.openSlots) != 1 {
			t.Fatal("waiter leaked a slot")
		}
		<-w.openSlots
	}
}
