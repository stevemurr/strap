# Session cache

The API gateway keeps a bounded number of recently seen session records in
memory so hot sessions skip the database. When the cache is full, the session
that has gone unused the longest is dropped.

## Contract

Package `sessioncache`, file `cache.go`:

```go
type Record struct {
    UserID  string
    Expires int64 // Unix seconds.
}

func New(capacity int) *Cache
func (c *Cache) Get(id string) (Record, bool)
func (c *Cache) Put(id string, r Record)
func (c *Cache) Len() int
```

- `New` returns an empty cache holding at most `capacity` records. It panics if
  `capacity < 1`.
- `Get` returns the record for `id` and marks that session as the most recently
  used. A miss returns the zero `Record` and `false` and does not change the
  cache.
- `Put` inserts or replaces the record for `id` and marks it as the most
  recently used. Replacing an existing id never evicts anything. Inserting into
  a full cache first evicts the least recently used record.
- "Used" means touched by `Get` or `Put`. `Len` does not count as use.
- `Len` returns the number of records currently held.
- `Get`, `Put` and `Len` must each run in constant time on average, independent
  of the capacity. The cache is used from one goroutine; no locking is needed.

## Example

```go
c := New(2)
c.Put("a", Record{UserID: "u1"})
c.Put("b", Record{UserID: "u2"})
c.Get("a")                        // a is now most recent
c.Put("c", Record{UserID: "u3"})  // evicts b
c.Get("b")                        // miss
c.Get("a")                        // hit: u1
c.Get("c")                        // hit: u3
c.Len()                           // 2
```

## Constraints

- Capacities up to 100,000 and millions of operations per run; a cache that
  scans its contents on each operation is too slow.
- Standard library only. Keep the package name and the exported API.
