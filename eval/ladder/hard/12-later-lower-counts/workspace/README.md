# Later lower counts

The price-history page shows, for each trading day, how many of the following
days closed at a lower price. Given the closing prices in date order, compute
that count for every day.

## Contract

Package `pricehistory`, file `counts.go`:

```go
func LaterLowerCounts(prices []int) []int
```

- The result has the same length as `prices`; `out[i]` is the number of
  indexes `j > i` with `prices[j] < prices[i]`. Equal prices do not count.
- Prices may be negative and may repeat.
- Returns a slice of length 0 for an empty input (`nil` is acceptable).
- The input must not be modified.

## Examples

| prices | result |
|---|---|
| `[5, 2, 6, 1]` | `[2, 1, 1, 0]` |
| `[3, 2, 1]` | `[2, 1, 0]` |
| `[1, 2, 3]` | `[0, 0, 0]` |
| `[-1, -1]` | `[0, 0]` |
| `[4]` | `[0]` |
| `[]` | `[]` |

## Constraints

- `len(prices)` can reach 500,000 with prices between -1,000,000,000 and
  1,000,000,000. The call must finish well under a second at that size;
  comparing every day with every later day is too slow.
- Standard library only. Keep the package name, file name and exported signature.
