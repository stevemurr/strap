package replay

import (
	"testing"
	"time"
)

// A timer fires only once the replay reaches its moment, never on the wall
// clock, and one already due fires at once.
func TestVirtualClockFiresOnlyWhenReached(t *testing.T) {
	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	c := newVirtualClock(start)
	fired, stop := c.Timer(start.Add(time.Second))
	defer stop()
	due, _ := c.Timer(start)
	select {
	case <-due:
	default:
		t.Fatal("a due timer did not fire")
	}
	c.advance(start.Add(500 * time.Millisecond))
	select {
	case <-fired:
		t.Fatal("fired before its moment")
	case <-time.After(20 * time.Millisecond):
	}
	c.advance(start.Add(time.Second))
	select {
	case at := <-fired:
		if !at.Equal(start.Add(time.Second)) || !c.Now().Equal(at) {
			t.Fatal(at, c.Now())
		}
	case <-time.After(time.Second):
		t.Fatal("did not fire at its moment")
	}
	c.advance(start) // Time never runs backwards.
	if !c.Now().Equal(start.Add(time.Second)) {
		t.Fatal(c.Now())
	}
}
