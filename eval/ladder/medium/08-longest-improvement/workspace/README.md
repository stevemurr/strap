# Longest improvement

The coaching app shows athletes how consistently they have improved. Given a
season's scores in chronological order, it reports the length of the longest
run of strictly improving scores, skipping over any number of off days in
between.

## Contract

Package `scores`, file `scores.go`:

```go
func LongestImprovement(scores []int) int
```

- Returns the length of the longest strictly increasing subsequence: a
  selection of scores in their original order, not necessarily adjacent, where
  each selected score is greater than the one selected before it.
- Equal scores do not count as an improvement, so `[5, 5, 5]` gives 1.
- Empty input returns 0; a single score returns 1.
- Scores may be negative or repeated.
- The input slice must not be modified.

## Examples

| scores | result |
|---|---|
| `[10, 9, 2, 5, 3, 7, 101, 18]` | 4 |
| `[0, 1, 0, 3, 2, 3]` | 4 |
| `[7, 7, 7]` | 1 |
| `[4, 3, 2, 1]` | 1 |
| `[-5, -3, -4, 0]` | 3 |
| `[]` | 0 |

## Constraints

- Up to 300,000 scores. The call must finish well under a second at that
  size; the classic solution that compares every pair of positions is too
  slow.
- Standard library only. Keep the package name, file name and exported signature.
