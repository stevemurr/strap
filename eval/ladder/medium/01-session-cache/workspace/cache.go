// Package sessioncache keeps the most recently used session records in memory.
package sessioncache

// Record is what the cache stores for a session.
type Record struct {
	UserID  string
	Expires int64 // Unix seconds.
}

// Cache is a fixed-capacity cache that evicts the least recently used record.
type Cache struct{}

// New returns an empty cache holding at most capacity records. It panics if
// capacity < 1.
func New(capacity int) *Cache {
	panic("not implemented")
}

// Get returns the record for id and marks the session as most recently used.
func (c *Cache) Get(id string) (Record, bool) {
	panic("not implemented")
}

// Put inserts or replaces the record for id and marks the session as most
// recently used, evicting the least recently used record when full.
func (c *Cache) Put(id string, r Record) {
	panic("not implemented")
}

// Len returns the number of records currently held.
func (c *Cache) Len() int {
	panic("not implemented")
}
