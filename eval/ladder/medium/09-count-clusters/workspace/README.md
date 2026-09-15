# Count clusters

The floor-plan tool colours each contiguous block of occupied desks so teams
can see how many separate pods sit on a floor. The floor is a grid in which
`#` marks an occupied desk and `.` an empty one.

## Contract

Package `occupancy`, file `occupancy.go`:

```go
func CountClusters(grid []string) int
```

- `grid` holds the rows top to bottom. Every row has the same length and
  contains only `#` and `.`.
- A cluster is a maximal set of occupied cells connected through horizontal
  or vertical neighbours. Diagonal neighbours do not connect.
- Returns the number of clusters.
- An empty grid, or a grid whose rows are empty strings, returns 0.
- The hidden tests never pass ragged rows or other characters.

## Examples

| grid | result |
|---|---|
| `["##..", "#...", "..#.", "...#"]` | 3 |
| `["#.#", ".#.", "#.#"]` | 5 |
| `["###", "###"]` | 1 |
| `["#.#.#"]` | 3 |
| `["...", "..."]` | 0 |
| `[]` | 0 |

## Constraints

- Grids up to 1,000 x 1,000 cells, including one that is entirely occupied
  (a single cluster of a million cells) and a checkerboard (half a million
  clusters). The call must finish well under a second at that size and must
  not overflow the stack; doing work proportional to the whole grid for every
  cluster found is too slow.
- Standard library only. Keep the package name, file name and exported signature.
