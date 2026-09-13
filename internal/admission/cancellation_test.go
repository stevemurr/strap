package admission_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stevemurr/strap/internal/admission"
)

// A lease is refused under a dead caller context, before the gate's own state
// is consulted.
func TestBeginHonorsCallerCancellation(t *testing.T) {
	gate := admission.New(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := gate.Begin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// The refused call did not consume a lease.
	run, done, err := gate.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.Err() != nil {
		t.Fatal("a fresh lease started cancelled")
	}
	done()
}

// Waiting for outstanding leases respects the caller's own deadline rather than
// blocking until the work happens to finish.
func TestWaitHonorsCallerDeadline(t *testing.T) {
	gate := admission.New(context.Background())
	_, done, err := gate.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gate.Seal()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Wait ignored the caller's deadline", err)
	}
	done()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A sealed gate never issues another lease.
	if _, _, err := gate.Begin(context.Background()); !errors.Is(err, admission.ErrClosed) {
		t.Fatal(err)
	}
}
