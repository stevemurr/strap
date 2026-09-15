# Balance point

A conveyor carries a row of parcels, and the loading software needs to know
where a single support could go so the row balances: the total weight of the
parcels to its left equals the total weight to its right. Weights are net of
packaging and can be negative for parcels lifted by a counterweight.

## Contract

Package `balance`, file `balance.go`:

```go
func Point(weights []int) int
```

- Returns the smallest index `i` such that the sum of `weights[:i]` equals
  the sum of `weights[i+1:]`. The parcel at `i` itself belongs to neither
  side. An empty side sums to `0`.
- Returns `-1` when no index qualifies, including for an empty input.
- A single-element input returns `0`: both sides are empty.
- Weights may be zero, negative and repeated.
- The input must not be modified.

## Examples

| weights | result |
|---|---|
| `[1, 7, 3, 6, 5, 6]` | `3` |
| `[1, 2, 3]` | `-1` |
| `[2, 1, -1]` | `0` |
| `[0, 0, 0]` | `0` |
| `[5]` | `0` |
| `[]` | `-1` |

## Constraints

- Up to 1,000,000 weights, each between -1,000,000 and 1,000,000. The call
  must finish well under a second at that size; re-adding both sides for every
  candidate index is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
