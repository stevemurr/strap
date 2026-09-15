package sessioncache

import (
	"fmt"
	"testing"
	"time"
)

func TestHiddenReadmeExample(t *testing.T) {
	c := New(2)
	c.Put("a", Record{UserID: "u1"})
	c.Put("b", Record{UserID: "u2"})
	if r, ok := c.Get("a"); !ok || r.UserID != "u1" {
		t.Fatalf("get a = %+v, %v", r, ok)
	}
	c.Put("c", Record{UserID: "u3"})
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if r, ok := c.Get("a"); !ok || r.UserID != "u1" {
		t.Fatalf("get a = %+v, %v", r, ok)
	}
	if r, ok := c.Get("c"); !ok || r.UserID != "u3" {
		t.Fatalf("get c = %+v, %v", r, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("len = %d", c.Len())
	}
}

func TestHiddenReplaceDoesNotEvict(t *testing.T) {
	c := New(2)
	c.Put("a", Record{UserID: "u1", Expires: 1})
	c.Put("b", Record{UserID: "u2"})
	c.Put("a", Record{UserID: "u1", Expires: 2})
	if c.Len() != 2 {
		t.Fatalf("len = %d", c.Len())
	}
	if r, ok := c.Get("b"); !ok || r.UserID != "u2" {
		t.Fatalf("b lost after replacing a: %+v, %v", r, ok)
	}
	if r, _ := c.Get("a"); r.Expires != 2 {
		t.Fatalf("a not replaced: %+v", r)
	}
	// a is now most recent, so inserting evicts b.
	c.Put("c", Record{})
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
}

func TestHiddenMissDoesNotTouchOrder(t *testing.T) {
	c := New(2)
	c.Put("a", Record{})
	c.Put("b", Record{})
	if _, ok := c.Get("zzz"); ok {
		t.Fatal("unexpected hit")
	}
	if c.Len() != 2 {
		t.Fatalf("len = %d", c.Len())
	}
	c.Put("c", Record{})
	if _, ok := c.Get("a"); ok {
		t.Fatal("a was least recently used and should be gone")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("b should remain")
	}
}

func TestHiddenLenIsNotUse(t *testing.T) {
	c := New(2)
	c.Put("a", Record{})
	c.Put("b", Record{})
	c.Get("a")
	_ = c.Len()
	c.Put("c", Record{})
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted; Len must not refresh recency")
	}
}

func TestHiddenCapacityOne(t *testing.T) {
	c := New(1)
	c.Put("a", Record{UserID: "1"})
	c.Put("b", Record{UserID: "2"})
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be evicted")
	}
	if r, ok := c.Get("b"); !ok || r.UserID != "2" {
		t.Fatalf("b = %+v, %v", r, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("len = %d", c.Len())
	}
}

func TestHiddenInvalidCapacityPanics(t *testing.T) {
	for _, capacity := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d) did not panic", capacity)
				}
			}()
			New(capacity)
		}()
	}
}

func TestHiddenEvictionOrderLongSequence(t *testing.T) {
	c := New(3)
	c.Put("a", Record{})
	c.Put("b", Record{})
	c.Put("c", Record{})
	c.Get("a")           // order: a c b
	c.Put("d", Record{}) // evicts b
	c.Get("c")           // order: c d a
	c.Put("e", Record{}) // evicts a
	for id, want := range map[string]bool{"a": false, "b": false, "c": true, "d": true, "e": true} {
		if _, ok := c.Get(id); ok != want {
			t.Errorf("get %s = %v, want %v", id, ok, want)
		}
	}
}

func TestHiddenConstantTime(t *testing.T) {
	const capacity = 100_000
	const ops = 3_000_000
	done := make(chan struct{})
	go func() {
		defer close(done)
		c := New(capacity)
		ids := make([]string, 2*capacity)
		for i := range ids {
			ids[i] = fmt.Sprintf("s%d", i)
		}
		for i := 0; i < ops; i++ {
			id := ids[(i*7919)%len(ids)]
			if i%3 == 0 {
				c.Put(id, Record{Expires: int64(i)})
			} else {
				c.Get(id)
			}
		}
		if c.Len() != capacity {
			panic(fmt.Sprintf("len = %d, want %d", c.Len(), capacity))
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%d operations on a %d-entry cache took longer than 10s", ops, capacity)
	}
}
