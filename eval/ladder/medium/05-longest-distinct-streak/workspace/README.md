# Longest distinct streak

The packet capture tool flags replayed traffic by looking for repeated packet
ids. As a quality signal it reports the longest stretch of consecutive packets
in which every id was unique. Given the ids in arrival order, compute the
length of that stretch.

## Contract

Package `packets`, file `packets.go`:

```go
func LongestDistinctStreak(ids []int) int
```

- A streak is a contiguous run `ids[i:j]` in which no id appears twice. Return
  the length of the longest streak.
- Empty input returns 0; a single id returns 1; if every id is the same the
  answer is 1.
- Ids may be negative, zero or repeated, and the same id may recur many times
  far apart.
- The input slice must not be modified.

## Examples

| ids | result |
|---|---|
| `[3, 1, 4, 1, 5, 9, 2, 6]` | 6 |
| `[7, 7, 7]` | 1 |
| `[1, 2, 3, 1, 2, 3]` | 3 |
| `[1, 2, 2, 1]` | 2 |
| `[-1, 0, -1, 2, 3]` | 4 |
| `[]` | 0 |

## Constraints

- Up to 1,000,000 ids, with distinct streaks that can span hundreds of
  thousands of packets or the whole input. The call must finish well under a
  second at that size; rescanning from every start position is too slow.
- Standard library only. Keep the package name, file name and exported signature.
