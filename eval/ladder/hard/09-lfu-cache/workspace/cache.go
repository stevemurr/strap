// Package lfucache keeps compiled templates in a bounded cache that evicts the
// least frequently used entry.
package lfucache

// Cache is a fixed-capacity cache that evicts the entry with the smallest use
// count, breaking ties by least recent use.
type Cache struct{}

// New returns an empty cache holding at most capacity entries. It panics if
// capacity < 1.
func New(capacity int) *Cache {
	panic("not implemented")
}

// Get returns the value for key and counts one use of it.
func (c *Cache) Get(key string) (int, bool) {
	panic("not implemented")
}

// Put inserts or replaces the value for key and counts one use of it,
// evicting the least frequently used entry when a new key does not fit.
func (c *Cache) Put(key string, value int) {
	panic("not implemented")
}

// Len returns the number of entries currently held.
func (c *Cache) Len() int {
	panic("not implemented")
}
