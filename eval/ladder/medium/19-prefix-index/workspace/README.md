# Prefix index

The search box suggests completions as the user types. The suggestion service
keeps the known query terms in an in-memory index that answers "is this exact
term known?", "does anything start with this?" and "how many terms start with
this?".

## Contract

Package `autocomplete`, file `index.go`:

```go
type Index struct { /* ... */ }

func New() *Index
func (x *Index) Add(term string)
func (x *Index) Contains(term string) bool
func (x *Index) HasPrefix(prefix string) bool
func (x *Index) CountPrefix(prefix string) int
```

- `New` returns an empty index.
- `Add` stores `term`. Adding a term that is already stored changes nothing.
  Terms are non-empty strings of lowercase ASCII letters `a`-`z`.
- `Contains` reports whether exactly `term` has been stored. A term is not
  implied by longer terms: after `Add("cart")`, `Contains("car")` is false.
- `HasPrefix` reports whether at least one stored term starts with `prefix`.
  Every term starts with `""`, so `HasPrefix("")` is true as soon as the index
  holds anything.
- `CountPrefix` returns how many distinct stored terms start with `prefix`;
  `CountPrefix("")` is the number of distinct terms stored. A term counts as
  its own prefix.
- Query arguments may be empty and otherwise contain only lowercase letters.
  `Contains("")` is false, since the empty term is never stored.
- The index is used from one goroutine; no locking is needed.

## Examples

Calls in sequence on one index:

| call | result |
|---|---|
| `x.Add("apple")` | |
| `x.Add("app")` | |
| `x.Add("apple")` | (no effect) |
| `x.Contains("app")` | `true` |
| `x.Contains("ap")` | `false` |
| `x.HasPrefix("ap")` | `true` |
| `x.HasPrefix("b")` | `false` |
| `x.CountPrefix("app")` | `2` |
| `x.CountPrefix("apple")` | `1` |
| `x.CountPrefix("apples")` | `0` |
| `x.CountPrefix("")` | `2` |

## Constraints

- The hidden tests add 100,000 distinct terms of 3 to 12 letters (some of them
  twice) and then run 200,000 mixed queries; everything together must finish
  within a few seconds. Each operation should cost time proportional to the
  length of its argument, not to the number of stored terms, so scanning the
  stored terms per query is far too slow.
- Standard library only. Keep the package name, file name and exported API.
