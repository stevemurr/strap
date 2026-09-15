# Pooled volume

The flood-model service takes a terrain profile as a row of columns, each one
unit wide with a non-negative height, and asks how much rain water would stay
pooled between the columns once the rest has run off either end.

## Contract

Package `terrain`, file `volume.go`:

```go
func PooledVolume(heights []int) int
```

- Column `i` has height `heights[i]` and is one unit wide. Water above column
  `i` rises to the smaller of the tallest column at or to its left and the
  tallest column at or to its right (both including column `i` itself); the
  water depth on that column is that level minus `heights[i]`. The result is
  the sum of the water depths over all columns.
- Heights are non-negative and may repeat. The hidden tests never pass a
  negative height.
- Profiles with fewer than three columns hold no water and return 0.
- The input must not be modified.

## Examples

| heights | result |
|---|---|
| `[0, 1, 0, 2, 1, 0, 1, 3, 2, 1, 2, 1]` | 6 |
| `[4, 2, 0, 3, 2, 5]` | 9 |
| `[3, 0, 3]` | 3 |
| `[5, 0, 0, 0, 5]` | 15 |
| `[1, 2, 3]` | 0 |
| `[2, 2]` | 0 |
| `[]` | 0 |

## Constraints

- Up to 5,000,000 columns with heights up to 1,000,000. The call must finish in
  well under a second at that size; scanning outward from every column to find
  its bounding walls is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
