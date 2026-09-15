// Package sessioncache keeps the most recently used session records in memory.
package sessioncache

import "container/list"

// Record is what the cache stores for a session.
type Record struct {
	UserID  string
	Expires int64 // Unix seconds.
}

type entry struct {
	id     string
	record Record
}

// Cache is a fixed-capacity cache that evicts the least recently used record.
// The list front is the most recently used entry.
type Cache struct {
	capacity int
	order    *list.List
	index    map[string]*list.Element
}

// New returns an empty cache holding at most capacity records. It panics if
// capacity < 1.
func New(capacity int) *Cache {
	if capacity < 1 {
		panic("sessioncache: capacity must be at least 1")
	}
	return &Cache{capacity: capacity, order: list.New(), index: make(map[string]*list.Element)}
}

// Get returns the record for id and marks the session as most recently used.
func (c *Cache) Get(id string) (Record, bool) {
	e, ok := c.index[id]
	if !ok {
		return Record{}, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*entry).record, true
}

// Put inserts or replaces the record for id and marks the session as most
// recently used, evicting the least recently used record when full.
func (c *Cache) Put(id string, r Record) {
	if e, ok := c.index[id]; ok {
		e.Value.(*entry).record = r
		c.order.MoveToFront(e)
		return
	}
	if c.order.Len() >= c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.index, oldest.Value.(*entry).id)
	}
	c.index[id] = c.order.PushFront(&entry{id: id, record: r})
}

// Len returns the number of records currently held.
func (c *Cache) Len() int {
	return c.order.Len()
}
