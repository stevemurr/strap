package admission

import (
	"context"
	"errors"
	"testing"
)

func TestCapacityAndCancellationRejectAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := New(ctx)
	g.limit = 1
	_, done, err := g.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Begin(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	done()
	cancel()
	if _, _, err := g.Begin(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	g.Seal()
	if err := g.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
