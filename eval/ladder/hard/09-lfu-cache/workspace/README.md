# LFU cache

The rendering service caches compiled templates keyed by template id. Memory
is tight, so when the cache is full it drops the template that has been used
least often, and among equally used templates the one that was used least
recently.

## Contract

Package `lfucache`, file `cache.go`:

```go
type Cache struct { /* ... */ }

func New(capacity int) *Cache
func (c *Cache) Get(key string) (int, bool)
func (c *Cache) Put(key string, value int)
func (c *Cache) Len() int
```

- `New` returns an empty cache holding at most `capacity` entries. It panics
  if `capacity < 1`.
- Every entry has a use count. `Get` returns the entry's value and `true` and
  adds one to its use count. A miss returns `(0, false)` and changes nothing.
- `Put` on a key already present replaces its value and adds one to its use
  count; it never evicts. `Put` on a new key inserts it with a use count of 1.
  If the cache is already full, the insert first evicts the entry with the
  smallest use count; among entries sharing that count, the one whose most
  recent use is the oldest goes.
- "Use" means a `Get` hit or a `Put` on that key. `Len` returns the number of
  entries and does not count as a use.
- `Get`, `Put` and `Len` must each take constant time on average, independent
  of the capacity and of how large use counts grow. The cache is used from one
  goroutine; no locking is needed.

## Example

```go
c := New(2)
c.Put("a", 1)
c.Put("b", 2)
c.Get("a")      // 1, true: a now has use count 2
c.Put("c", 3)   // full; b (count 1) is evicted
c.Get("b")      // 0, false
c.Get("c")      // 3, true: c now has use count 2
c.Put("d", 4)   // a and c both have count 2; a was used less recently, so a goes
c.Get("a")      // 0, false
c.Get("c")      // 3, true
c.Get("d")      // 4, true
c.Len()         // 2
```

## Constraints

- Capacities up to 50,000 and millions of operations per run. Scanning the
  entries to find the eviction victim, or keeping every entry in one list
  ordered by use count, is too slow.
- Standard library only. Keep the package name and the exported API.
