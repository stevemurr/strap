# Smallest covering span

The incident tooling replays an application's event log and finds the shortest
stretch of consecutive events that contains a given mix of event types, so an
engineer can look at the tightest window that reproduces a failure.

## Contract

Package `logspan`, file `span.go`:

```go
func SmallestCoveringSpan(events []string, required []string) (start, end int, ok bool)
```

- `events` is the log in order; each entry is an event type. `required` lists
  event types, possibly with repeats: a type that appears `k` times in
  `required` must appear at least `k` times inside the span.
- Returns the half-open range `events[start:end]` of smallest length that
  covers `required`, with `ok == true`. Events of types that are not required
  may appear inside the span; they simply count toward its length.
- When several spans of the smallest length qualify, return the one with the
  smallest `start`.
- Returns `(0, 0, false)` when `required` is empty or when no span covers it.
- Event types are compared as exact strings, case-sensitively. Neither input
  may be modified.

## Examples

The examples abbreviate event types to single letters.

| events | required | result |
|---|---|---|
| `[a d o b e c o d e b a n c]` | `[a b c]` | `(9, 13, true)` |
| `[x a x b a x]` | `[a b]` | `(3, 5, true)` |
| `[a b a b]` | `[a b]` | `(0, 2, true)` |
| `[a a]` | `[a a]` | `(0, 2, true)` |
| `[a]` | `[a a]` | `(0, 0, false)` |
| `[a b]` | `[]` | `(0, 0, false)` |

## Constraints

- Up to 1,000,000 events drawn from a few dozen types, with up to 50 required
  entries (duplicates included). The call must finish in well under a second at
  that size; trying every pair of endpoints, or growing a fresh span from every
  start position, is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
