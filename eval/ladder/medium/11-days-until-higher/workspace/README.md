# Days until higher

The pricing dashboard shows, for each trading day of a listing, how many days
the seller had to wait before the listing closed at a strictly higher price.
Given the daily closing prices in order, compute that wait for every day.

## Contract

Package `prices`, file `prices.go`:

```go
func DaysUntilHigher(prices []int) []int
```

- The result has one entry per input day: `out[i]` is the smallest `d > 0`
  such that `prices[i+d] > prices[i]`, i.e. the number of days after day `i`
  until the first strictly higher price.
- `out[i]` is `0` when no later day has a strictly higher price. An equal price
  does not count.
- An empty input produces an empty result (`nil` or zero length).
- Prices may be negative and may repeat.
- The input must not be modified.

## Examples

| prices | result |
|---|---|
| `[73, 74, 75, 71, 69, 72, 76, 73]` | `[1, 1, 4, 2, 1, 1, 0, 0]` |
| `[30, 40, 50, 60]` | `[1, 1, 1, 0]` |
| `[30, 60, 90]` | `[1, 1, 0]` |
| `[5, 5, 5]` | `[0, 0, 0]` |
| `[9, 8, 7, 6]` | `[0, 0, 0, 0]` |
| `[]` | `[]` |

## Constraints

- Up to 1,000,000 days. The hidden tests include a million strictly decreasing
  prices; the call must finish well under a second, so scanning forward from
  each day until a higher price appears is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
