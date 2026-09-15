# Earliest crossing

The flood-planning tool models a district as a grid of cells with integer
elevations. The water level rises one unit per hour, and a rescue boat can
float over any cell whose elevation does not exceed the current level. The
team needs the earliest hour at which the boat can travel from the top-left
cell of the grid to the bottom-right cell.

## Contract

Package `floodmap`, file `crossing.go`:

```go
func EarliestCrossing(elevation [][]int) int
```

- `elevation[r][c]` is the elevation of row `r`, column `c`; all rows have the
  same length and elevations are non-negative integers (they may repeat).
- At time `t` the boat may occupy any cell with `elevation <= t` and may move
  any number of times between cells that share an edge (up, down, left,
  right), as long as both cells satisfy the limit. Return the smallest `t` for
  which a route exists from cell `(0, 0)` to cell `(R-1, C-1)`. Equivalently,
  over all routes, the minimum of the highest elevation on the route,
  including both end cells.
- A grid with a single cell returns that cell's elevation.
- A grid with no cells (no rows, or rows of length 0) returns `0`.
- The input must not be modified.

## Examples

| elevation | result |
|---|---|
| `[[0, 2], [1, 3]]` | 3 |
| `[[0, 1, 2, 3, 4], [24, 23, 22, 21, 5], [12, 13, 14, 15, 16], [11, 17, 18, 19, 20], [10, 9, 8, 7, 6]]` | 16 |
| `[[0, 9, 0], [0, 9, 0], [0, 0, 0]]` | 0 |
| `[[3, 0], [0, 0]]` | 3 |
| `[[5]]` | 5 |
| `[]` | 0 |

## Constraints

- Up to 500 rows and 500 columns, with elevations up to 1,000,000. The call
  must finish well under a second at that size; testing every water level in
  turn, or a shortest-path search that scans every cell to pick the next one,
  is too slow.
- Standard library only. Keep the package name, file name and exported signature.
