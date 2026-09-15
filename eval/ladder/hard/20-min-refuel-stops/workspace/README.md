# Minimum refuel stops

The route planner estimates how many fuel stops a delivery van needs on a
highway run. The van starts with a known amount of fuel, burns one unit per
unit of distance, and can top up at stations along the way, each of which has
a fixed amount available; the tank has no capacity limit. Find the fewest stops
that get the van to its destination.

## Contract

Package `roadtrip`, file `refuel.go`:

```go
func MinRefuelStops(target, startFuel int, stations [][2]int) int
```

- `target >= 1` is the distance from the start (position 0) to the
  destination; `startFuel >= 0` is the fuel in the tank at the start.
- `stations[i] = [position, fuel]` with `0 < position < target` and
  `fuel >= 0`. Stations are sorted by strictly increasing position. Stopping
  at a station adds all of its fuel to the tank; the van may stop at any
  subset of the stations it can reach, in road order.
- The van can reach position `p` when the fuel obtained so far (the start
  fuel plus the fuel from every station it has stopped at) is at least `p`.
  Arriving with exactly zero fuel counts, both at a station and at the
  destination.
- Return the minimum number of stops that lets the van reach `target`, or
  `-1` if no choice of stops does. `startFuel >= target` returns `0`
  regardless of the stations.
- The input must not be modified.

## Examples

| target | startFuel | stations | result |
|---|---|---|---|
| 1 | 1 | `[]` | 0 |
| 100 | 1 | `[[10, 100]]` | -1 |
| 100 | 10 | `[[10, 60], [20, 30], [30, 30], [60, 40]]` | 2 |
| 100 | 50 | `[[25, 25], [50, 50]]` | 1 |
| 10 | 3 | `[[3, 3], [6, 4]]` | 2 |
| 10 | 5 | `[[2, 0], [5, 0]]` | -1 |

## Constraints

- Up to 500,000 stations with positions, fuel amounts and `target` up to
  1,000,000,000; totals fit in an `int`. The call must finish well under a
  second at that size; a table with an entry per station per stop count is
  too slow.
- Standard library only. Keep the package name, file name and exported signature.
