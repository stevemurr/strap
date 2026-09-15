# Minimum team cost

The contractor marketplace staffs a job with exactly k workers. Each worker
has a quality score and a minimum wage they will accept. A hired team is paid
at a single rate per unit of quality, so a worker with twice the quality earns
twice the pay, and nobody may be paid less than their minimum. Find the
cheapest possible team.

## Contract

Package `hiring`, file `team.go`:

```go
func MinTeamCost(quality, wage []int, k int) float64
```

- `quality[i] >= 1` and `wage[i] >= 1` are integers and
  `len(quality) == len(wage)`.
- Choose exactly `k` workers and a single real-valued rate `r` such that every
  hired worker `i` receives `r * quality[i] >= wage[i]`. The team cost is the
  sum of `r * quality[i]` over the hired workers. Return the smallest cost
  achievable over all choices of team and rate.
- Equivalently: for a given team, the cheapest valid rate is the largest
  `wage[i] / quality[i]` among its members, and the cost is that rate times
  the sum of the members' qualities.
- Returns `0` when `k < 1` or `k > len(quality)`.
- Results are compared with an absolute tolerance of `1e-6`.
- The inputs must not be modified.

## Examples

| quality | wage | k | result |
|---|---|---|---|
| `[10, 20, 5]` | `[70, 50, 30]` | 2 | 105.0 |
| `[3, 1, 10, 10, 1]` | `[4, 8, 2, 2, 7]` | 3 | 30.666667 |
| `[4, 2]` | `[8, 3]` | 2 | 12.0 |
| `[5]` | `[7]` | 1 | 7.0 |
| `[1, 2, 3]` | `[1, 1, 1]` | 4 | 0 |

In the first example workers 0 and 2 are hired at rate 7 per quality point:
worker 0 gets 70 and worker 2 gets 35.

## Constraints

- Up to 100,000 workers with qualities up to 100,000 and wages up to
  10,000,000, and any `k` up to the number of workers. The call must finish
  well under a second at that size; trying every worker as the one who sets
  the rate and re-scanning the others each time is too slow.
- Standard library only. Keep the package name, file name and exported signature.
