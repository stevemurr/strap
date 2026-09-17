package admission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stevemurr/strap/internal/admission"
)

func TestSuspendJoinsExistingOperationsAndSealCannotBeResumed(t *testing.T) {
	g := admission.New(context.Background())
	run, done, err := g.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	drained := g.Suspend()
	if !errors.Is(run.Err(), context.Canceled) {
		t.Fatal("suspension did not cancel admitted operation")
	}
	if g.Suspend() != drained {
		t.Fatal("repeated suspension replaced outstanding wait")
	}
	if _, _, err := g.Begin(context.Background()); !errors.Is(err, admission.ErrSuspended) {
		t.Fatal(err)
	}
	select {
	case <-drained:
		t.Fatal("unsettled operation was not joined")
	default:
	}
	done()
	<-drained
	g.Resume()
	_, done, err = g.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done()
	<-g.Suspend()
	g.Seal()
	g.Resume()
	if _, _, err := g.Begin(context.Background()); !errors.Is(err, admission.ErrClosed) {
		t.Fatal("resumed a sealed gate", err)
	}
	if err := g.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
