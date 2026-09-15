# Longest descent

The trail-planning tool works on an elevation grid and wants the longest
possible downhill run: a walk that steps between edge-adjacent cells and loses
elevation at every step.

## Contract

Package `trails`, file `descent.go`:

```go
func LongestDescent(elevation [][]int) int
```

- `elevation` is rectangular: every row has the same length. Elevations may
  be negative and may repeat.
- A trail is a sequence of cells where each cell is a 4-neighbour (up, down,
  left or right; no diagonals) of the previous one and has strictly lower
  elevation than it. A trail may start and end on any cell.
- Returns the number of cells on the longest trail. A grid with at least one
  cell always returns at least 1, because a single cell is a trail.
- Returns 0 for an empty grid: no rows, or rows with no cells.
- The input must not be modified.

## Examples

| elevation | result |
|---|---|
| `[[9, 9, 4], [6, 6, 8], [2, 1, 1]]` | 4 |
| `[[3, 4, 5], [3, 2, 6], [2, 2, 1]]` | 4 |
| `[[1, 2], [4, 3]]` | 4 |
| `[[7, 7], [7, 7]]` | 1 |
| `[[1]]` | 1 |
| `[]` | 0 |

## Constraints

- Grids up to 500 x 500 (250,000 cells), including grids where a single trail
  winds through every cell and grids that contain an astronomical number of
  distinct trails. The call must finish in well under a second at that size;
  re-exploring a cell every time a different trail reaches it is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
