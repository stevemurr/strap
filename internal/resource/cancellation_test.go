package resource_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stevemurr/strap/internal/resource"
)

type counted struct {
	closed int
	err    error
}

func (c *counted) Close(context.Context) error { c.closed++; return c.err }

// Closing a group under a dead context stops before touching anything, so the
// caller keeps ownership of what it asked to release.
func TestCloseHonorsCallerCancellation(t *testing.T) {
	group := resource.New()
	first := &counted{}
	group.Add("first", first)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := group.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if first.closed != 0 {
		t.Fatal("a cancelled close released a resource anyway", first.closed)
	}
	if err := group.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first.closed != 1 {
		t.Fatal(first.closed)
	}
}

// A resource that fails to close is reported, is not marked released, and does
// not stop the rest of the group from being released.
func TestCloseReportsFailuresAndRetriesThem(t *testing.T) {
	group := resource.New()
	broken := &counted{err: errors.New("still busy")}
	ok := &counted{}
	group.Add("ok", ok)
	group.Add("broken", broken)
	err := group.Close(context.Background())
	if err == nil {
		t.Fatal("a failed close was reported as success")
	}
	if ok.closed != 1 {
		t.Fatal("one failure stopped the rest of the group", ok.closed)
	}
	// Closing again retries only what has not been released.
	broken.err = nil
	if err := group.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if broken.closed != 2 || ok.closed != 1 {
		t.Fatal("close retried the wrong resources", broken.closed, ok.closed)
	}
}
