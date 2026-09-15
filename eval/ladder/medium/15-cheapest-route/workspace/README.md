# Cheapest route

The delivery planner models a district as a grid of blocks; entering a block
costs its toll. A courier starts at the top-left block and has to reach the
bottom-right block moving only right or down. Find the cheapest total toll.

## Contract

Package `tolls`, file `route.go`:

```go
func CheapestRoute(toll [][]int) int
```

- `toll` is rectangular: every row has the same length. Every toll is `>= 0`.
- A route starts at `toll[0][0]`, ends at `toll[m-1][n-1]`, and every step
  moves one block right or one block down. Its cost is the sum of the tolls of
  every block it visits, including both end blocks.
- Returns the minimum cost over all routes.
- Returns `0` for an empty grid: no rows, or rows of length zero.
- A single block is its own route: the result is that block's toll.
- Sums fit in an `int`. The input must not be modified.

## Examples

| toll | result |
|---|---|
| `[[1, 3, 1], [1, 5, 1], [4, 2, 1]]` | `7` |
| `[[1, 2, 3], [4, 5, 6]]` | `12` |
| `[[5]]` | `5` |
| `[[1, 2, 3]]` | `6` |
| `[[0, 0], [0, 0]]` | `0` |
| `[]` | `0` |

## Constraints

- Grids up to 2,000 x 2,000. The call must finish well under a second at that
  size; exploring routes recursively without reusing results is hopeless, since
  the number of routes is astronomical.
- Standard library only. Keep the package name, file name and exported signature.
