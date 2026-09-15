# Find in rotated

The appliance's crash log is a fixed ring buffer of event ids. Ids are
assigned in increasing order and the buffer wraps around, so reading it from
slot 0 gives an ascending sequence that has been rotated: the newest ids sit
in front of the oldest ones. Support tooling looks up which slot holds an id.

## Contract

Package `ringlog`, file `find.go`:

```go
func Find(ids []int, target int) int
```

- `ids` holds distinct integers that would be strictly ascending if the slice
  were rotated left by some offset in `[0, len(ids))`. Offset `0`, a plain
  sorted slice, is possible. For example `[4, 5, 6, 7, 0, 1, 2]` is
  `[0, 1, 2, 4, 5, 6, 7]` rotated left by 3.
- Returns the index `i` with `ids[i] == target`, or `-1` when the id is absent.
- An empty `ids` gives `-1`. Ids and the target may be negative.
- The input must not be modified.

## Examples

| ids | target | result |
|---|---|---|
| `[4, 5, 6, 7, 0, 1, 2]` | 0 | `4` |
| `[4, 5, 6, 7, 0, 1, 2]` | 3 | `-1` |
| `[1, 2, 3, 4, 5]` | 5 | `4` |
| `[3, 1]` | 1 | `1` |
| `[7]` | 7 | `0` |
| `[]` | 9 | `-1` |

## Constraints

- Up to 1,000,000 ids and 200,000 lookups against the same slice per hidden
  test run. All lookups together must finish within a few seconds, so each
  lookup has to take logarithmic time rather than scanning the slice.
- Standard library only. Keep the package name, file name and exported signature.
