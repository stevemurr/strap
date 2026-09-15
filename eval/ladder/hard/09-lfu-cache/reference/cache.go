// Package lfucache keeps compiled templates in a bounded cache that evicts the
// least frequently used entry.
package lfucache

import "container/list"

type entry struct {
	key   string
	value int
	uses  int
}

// Cache is a fixed-capacity cache that evicts the entry with the smallest use
// count, breaking ties by least recent use. Entries with the same use count
// share one list whose front is the most recently used entry, and minUses
// names the lowest count that still has a list.
type Cache struct {
	capacity int
	items    map[string]*list.Element
	byUses   map[int]*list.List
	minUses  int
}

// New returns an empty cache holding at most capacity entries. It panics if
// capacity < 1.
func New(capacity int) *Cache {
	if capacity < 1 {
		panic("lfucache: capacity must be at least 1")
	}
	return &Cache{capacity: capacity, items: make(map[string]*list.Element), byUses: make(map[int]*list.List)}
}

// Get returns the value for key and counts one use of it.
func (c *Cache) Get(key string) (int, bool) {
	e, ok := c.items[key]
	if !ok {
		return 0, false
	}
	ent := e.Value.(*entry)
	c.touch(e, ent)
	return ent.value, true
}

// Put inserts or replaces the value for key and counts one use of it,
// evicting the least frequently used entry when a new key does not fit.
func (c *Cache) Put(key string, value int) {
	if e, ok := c.items[key]; ok {
		ent := e.Value.(*entry)
		ent.value = value
		c.touch(e, ent)
		return
	}
	if len(c.items) >= c.capacity {
		bucket := c.byUses[c.minUses]
		victim := bucket.Remove(bucket.Back()).(*entry)
		delete(c.items, victim.key)
		if bucket.Len() == 0 {
			delete(c.byUses, c.minUses)
		}
	}
	c.items[key] = c.bucket(1).PushFront(&entry{key: key, value: value, uses: 1})
	c.minUses = 1
}

// Len returns the number of entries currently held.
func (c *Cache) Len() int {
	return len(c.items)
}

// touch moves an entry from its current use-count list to the front of the
// next one, dropping the old list when it empties.
func (c *Cache) touch(e *list.Element, ent *entry) {
	bucket := c.byUses[ent.uses]
	bucket.Remove(e)
	if bucket.Len() == 0 {
		delete(c.byUses, ent.uses)
		if c.minUses == ent.uses {
			c.minUses++
		}
	}
	ent.uses++
	c.items[ent.key] = c.bucket(ent.uses).PushFront(ent)
}

func (c *Cache) bucket(uses int) *list.List {
	b, ok := c.byUses[uses]
	if !ok {
		b = list.New()
		c.byUses[uses] = b
	}
	return b
}
