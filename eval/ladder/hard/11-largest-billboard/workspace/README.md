# Largest billboard

The outdoor-advertising planner models a street front as a row of adjacent
building slots, each one unit wide with a known height. A billboard is an
axis-aligned rectangle that must fit entirely under the skyline: it spans a
contiguous run of slots and can be no taller than the lowest slot in that run.
Find the largest area a billboard can have.

## Contract

Package `skyline`, file `billboard.go`:

```go
func LargestBillboard(heights []int) int
```

- `heights[i]` is the height of slot `i`; every slot is one unit wide. Heights
  are non-negative integers and may repeat.
- A billboard covering slots `l..r` (inclusive) has width `r-l+1` and height
  `min(heights[l..r])`; its area is the product. Return the maximum area over
  all `0 <= l <= r < len(heights)`.
- Returns `0` for an empty slice and when every height is `0`.
- The input must not be modified.

## Examples

| heights | result |
|---|---|
| `[2, 1, 5, 6, 2, 3]` | 10 |
| `[2, 4]` | 4 |
| `[5, 5, 5]` | 15 |
| `[3, 0, 3]` | 3 |
| `[7]` | 7 |
| `[]` | 0 |

## Constraints

- `len(heights)` can reach 1,000,000 with heights up to 1,000,000, so the
  answer can exceed 32 bits (`int` is 64-bit on the test machines). The call
  must finish well under a second at that size; trying every pair of slots, or
  scanning outward from every slot until a lower one appears, is too slow.
- Standard library only. Keep the package name, file name and exported signature.
