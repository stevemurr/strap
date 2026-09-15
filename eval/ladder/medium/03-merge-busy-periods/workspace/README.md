# Merge busy periods

The scheduling service shows a person's availability by combining the busy
periods from all of their calendars. Overlapping meetings and back-to-back
meetings should collapse into one busy block so the free/busy view stays
simple.

## Contract

Package `calendar`, file `calendar.go`:

```go
func MergeBusy(periods [][2]int) [][2]int
```

- Each period is a closed interval `[start, end]` in minutes with
  `start <= end`; a period with `start == end` is a single instant and still
  counts as busy.
- Two periods merge when they overlap or touch. Touching means one period ends
  exactly when the other starts: `[1, 10]` and `[10, 20]` merge into `[1, 20]`,
  but `[1, 10]` and `[11, 20]` stay separate.
- Merging is transitive: a chain of pairwise overlapping or touching periods
  becomes one block, and a period inside another disappears into it.
- The result holds the merged blocks sorted by start. Consecutive blocks never
  overlap or touch, so `result[i][1] < result[i+1][0]`.
- The input is in no particular order, may contain duplicates and negative
  values, and must not be modified.
- Empty input returns a zero-length result (nil is fine).

## Examples

| periods | result |
|---|---|
| `[[8, 10], [1, 3], [2, 6], [15, 18]]` | `[[1, 6], [8, 10], [15, 18]]` |
| `[[1, 4], [4, 5]]` | `[[1, 5]]` |
| `[[1, 10], [11, 20]]` | `[[1, 10], [11, 20]]` |
| `[[1, 3], [2, 4], [3, 5]]` | `[[1, 5]]` |
| `[[-5, -1], [-3, 2], [7, 7]]` | `[[-5, 2], [7, 7]]` |
| `[[1, 10], [2, 3]]` | `[[1, 10]]` |
| `[]` | `[]` |

## Constraints

- Up to 1,000,000 periods spread over a range of about a billion minutes. The
  call must finish well under a second at that size; comparing every period
  against every other is too slow.
- Standard library only. Keep the package name, file name and exported signature.
