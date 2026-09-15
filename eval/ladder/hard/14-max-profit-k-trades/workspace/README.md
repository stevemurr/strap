# Max profit with k trades

The backtesting service scores a simple strategy against a daily price series:
buy one share, sell it on a later day, and repeat at most k times while never
holding more than one share. Given the series and k, report the best total
profit the strategy could have made with perfect hindsight.

## Contract

Package `trading`, file `profit.go`:

```go
func MaxProfit(prices []int, k int) int
```

- `prices[i]` is the price on day `i`; prices are non-negative integers.
- A trade buys on day `b` and sells on day `s > b`, earning
  `prices[s] - prices[b]`. Trades must not overlap: the next purchase has to
  happen on a later day than the previous sale.
- At most `k` trades may be made; making fewer (including none) is allowed, so
  the result is never negative.
- Returns `0` when `k < 1` or `len(prices) < 2`.
- `k` may be far larger than the number of days; the answer is then the same
  as with unlimited trades, and the call must not spend time or memory
  proportional to `k * len(prices)`.
- The input must not be modified.

## Examples

| prices | k | result |
|---|---|---|
| `[2, 4, 1]` | 2 | 2 |
| `[3, 2, 6, 5, 0, 3]` | 2 | 7 |
| `[1, 2, 3, 4, 5]` | 2 | 4 |
| `[7, 6, 4, 3, 1]` | 3 | 0 |
| `[1, 5, 2, 8]` | 1 | 7 |
| `[1, 5, 2, 8]` | 100 | 10 |
| `[5]` | 1 | 0 |

## Constraints

- Up to 100,000 days with prices up to 1,000,000, and `k` up to 1,000,000.
  The call must finish well under a second both for `k = 50` and for
  `k = 1,000,000`; enumerating combinations of trades, or filling a table with
  an entry per day per allowed trade when `k` is huge, is too slow.
- Standard library only. Keep the package name, file name and exported signature.
