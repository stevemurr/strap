package lfucache

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func expectGet(t *testing.T, c *Cache, key string, value int, ok bool) {
	t.Helper()
	if v, got := c.Get(key); v != value || got != ok {
		t.Fatalf("Get(%q) = (%d, %v), want (%d, %v)", key, v, got, value, ok)
	}
}

func TestHiddenReadmeExample(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	expectGet(t, c, "a", 1, true)
	c.Put("c", 3)
	expectGet(t, c, "b", 0, false)
	expectGet(t, c, "c", 3, true)
	c.Put("d", 4)
	expectGet(t, c, "a", 0, false)
	expectGet(t, c, "c", 3, true)
	expectGet(t, c, "d", 4, true)
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
}

func TestHiddenReplaceBumpsAndNeverEvicts(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("a", 10) // a: count 2, b: count 1
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	c.Put("c", 3) // evicts b
	expectGet(t, c, "b", 0, false)
	expectGet(t, c, "a", 10, true)
	expectGet(t, c, "c", 3, true)
}

func TestHiddenMissChangesNothing(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	expectGet(t, c, "zzz", 0, false)
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	c.Put("c", 3) // a and b both count 1; a is older
	expectGet(t, c, "a", 0, false)
	expectGet(t, c, "b", 2, true)
}

func TestHiddenLenIsNotUse(t *testing.T) {
	c := New(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a")
	for i := 0; i < 5; i++ {
		_ = c.Len()
	}
	c.Put("c", 3)
	expectGet(t, c, "b", 0, false)
	expectGet(t, c, "a", 1, true)
}

func TestHiddenFrequencyBeatsRecency(t *testing.T) {
	c := New(3)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	c.Get("a")
	c.Get("a")
	c.Get("b")
	c.Get("c")    // counts: a 3, b 2, c 2; c most recent
	c.Put("d", 4) // evicts b: lowest count, older than c
	expectGet(t, c, "b", 0, false)
	expectGet(t, c, "a", 1, true)
	expectGet(t, c, "c", 3, true)
	expectGet(t, c, "d", 4, true)
	// d has count 2 now; a 4, c 3. Insert evicts d.
	c.Put("e", 5)
	expectGet(t, c, "d", 0, false)
	expectGet(t, c, "e", 5, true)
}

func TestHiddenTieBrokenByRecency(t *testing.T) {
	c := New(3)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	c.Get("b")
	c.Get("c")
	c.Get("a") // all count 2; b is least recent
	c.Put("d", 4)
	expectGet(t, c, "b", 0, false)
	expectGet(t, c, "c", 3, true) // c: 3, a: 2, d: 1
	c.Put("e", 5)                 // evicts d
	expectGet(t, c, "d", 0, false)
	expectGet(t, c, "a", 1, true)
	expectGet(t, c, "e", 5, true)
}

func TestHiddenCapacityOne(t *testing.T) {
	c := New(1)
	c.Put("a", 1)
	c.Get("a")
	c.Get("a")
	c.Put("b", 2) // a goes despite its higher count: it is the only entry
	expectGet(t, c, "a", 0, false)
	expectGet(t, c, "b", 2, true)
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
}

func TestHiddenEmptyCache(t *testing.T) {
	c := New(3)
	if c.Len() != 0 {
		t.Fatalf("Len = %d, want 0", c.Len())
	}
	expectGet(t, c, "a", 0, false)
}

func TestHiddenInvalidCapacityPanics(t *testing.T) {
	for _, capacity := range []int{0, -1, -100} {
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

// model is a slow LFU cache that scans every entry on eviction.
type model struct {
	capacity int
	entries  []*modelEntry
	tick     int
}

type modelEntry struct {
	key      string
	value    int
	uses     int
	lastUsed int
}

func (m *model) find(key string) *modelEntry {
	for _, e := range m.entries {
		if e.key == key {
			return e
		}
	}
	return nil
}

func (m *model) get(key string) (int, bool) {
	e := m.find(key)
	if e == nil {
		return 0, false
	}
	m.tick++
	e.uses++
	e.lastUsed = m.tick
	return e.value, true
}

func (m *model) put(key string, value int) {
	m.tick++
	if e := m.find(key); e != nil {
		e.value = value
		e.uses++
		e.lastUsed = m.tick
		return
	}
	if len(m.entries) >= m.capacity {
		victim := 0
		for i, e := range m.entries {
			v := m.entries[victim]
			if e.uses < v.uses || (e.uses == v.uses && e.lastUsed < v.lastUsed) {
				victim = i
			}
		}
		m.entries = append(m.entries[:victim], m.entries[victim+1:]...)
	}
	m.entries = append(m.entries, &modelEntry{key: key, value: value, uses: 1, lastUsed: m.tick})
}

func TestHiddenAgainstModel(t *testing.T) {
	rng := rand.New(rand.NewSource(460))
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for round := 0; round < 60; round++ {
		capacity := rng.Intn(5) + 1
		c := New(capacity)
		m := &model{capacity: capacity}
		for op := 0; op < 400; op++ {
			key := keys[rng.Intn(len(keys))]
			if rng.Intn(3) == 0 {
				value := rng.Intn(100)
				c.Put(key, value)
				m.put(key, value)
			} else {
				gotV, gotOK := c.Get(key)
				wantV, wantOK := m.get(key)
				if gotV != wantV || gotOK != wantOK {
					t.Fatalf("round %d op %d: Get(%q) = (%d, %v), want (%d, %v)", round, op, key, gotV, gotOK, wantV, wantOK)
				}
			}
			if c.Len() != len(m.entries) {
				t.Fatalf("round %d op %d: Len = %d, want %d", round, op, c.Len(), len(m.entries))
			}
		}
	}
}

func TestHiddenConstantTime(t *testing.T) {
	const capacity = 50_000
	const ops = 2_000_000
	keys := make([]string, 2*capacity)
	for i := range keys {
		keys[i] = fmt.Sprintf("tpl-%d", i)
	}
	done := make(chan string, 1)
	go func() {
		c := New(capacity)
		x := uint32(9)
		hits := 0
		for i := 0; i < ops; i++ {
			x = x*1664525 + 1013904223
			key := keys[int(x>>8)%len(keys)]
			if i%3 == 0 {
				c.Put(key, i)
			} else if _, ok := c.Get(key); ok {
				hits++
			}
		}
		if c.Len() != capacity {
			done <- fmt.Sprintf("Len = %d, want %d", c.Len(), capacity)
			return
		}
		if hits == 0 {
			done <- "no Get ever hit"
			return
		}
		done <- ""
	}()
	select {
	case msg := <-done:
		if msg != "" {
			t.Fatal(msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%d operations on a %d-entry cache took longer than 10s", ops, capacity)
	}
}
