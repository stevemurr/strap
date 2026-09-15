# Starting depot

A delivery van drives a circular route of `n` depots. Arriving at depot `i` it
picks up `fuel[i]` litres, and driving from depot `i` to the next depot
(`i+1`, wrapping from the last depot back to depot `0`) burns `cost[i]` litres.
The van starts with an empty tank at a depot of our choosing and has to get
all the way round back to that depot without running dry.

## Contract

Package `route`, file `depot.go`:

```go
func StartingDepot(fuel, cost []int) int
```

- `len(fuel) == len(cost) >= 1`; every value is `>= 0`.
- Starting at depot `s` with an empty tank the van gains `fuel[s]`, spends
  `cost[s]` to reach the next depot, gains that depot's fuel, and so on for `n`
  legs until it is back at `s`. The start is feasible when the tank is never
  negative after any leg; reaching exactly zero is fine.
- Returns the smallest feasible starting depot index, or `-1` when no start is
  feasible. When total fuel is at least total cost some start is always
  feasible. It is usually unique, but a stretch of the route that exactly
  breaks even can make several depots feasible, hence the smallest-index rule.
- A single depot is feasible exactly when `fuel[0] >= cost[0]`.
- Sums fit in an `int`. The inputs must not be modified.

## Examples

| fuel | cost | result |
|---|---|---|
| `[1, 2, 3, 4, 5]` | `[3, 4, 5, 1, 2]` | `3` |
| `[2, 3, 4]` | `[3, 4, 3]` | `-1` |
| `[5, 1, 2, 3, 4]` | `[4, 4, 1, 5, 1]` | `4` |
| `[3]` | `[3]` | `0` |
| `[2]` | `[3]` | `-1` |
| `[1, 1]` | `[1, 1]` | `0` |

## Constraints

- Up to 1,000,000 depots. The hidden tests use routes where almost every start
  gets most of the way round before running dry; the call must finish well
  under a second, so simulating the loop from each candidate start is far too
  slow.
- Standard library only. Keep the package name, file name and exported signature.
