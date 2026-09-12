package resource

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type closer func(context.Context) error

func (f closer) Close(ctx context.Context) error { return f(ctx) }

func TestCloseRetriesOnlyUnfinishedResourcesInReverseOrder(t *testing.T) {
	g := New()
	var calls []string
	failure := errors.New("retry cleanup")
	failed := false
	g.Add("first", closer(func(context.Context) error {
		calls = append(calls, "first")
		if !failed {
			failed = true
			return failure
		}
		return nil
	}))
	g.Add("second", closer(func(context.Context) error { calls = append(calls, "second"); return nil }))
	if err := g.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"second", "first", "first"}) {
		t.Fatal(calls)
	}
}
