# Best single trade

The backtesting notebook asks, for one instrument's daily closing prices, what
the best possible outcome of a single round trip would have been: buy on one
day, sell on a later day, and never hold more than one position.

## Contract

Package `trading`, file `trade.go`:

```go
func BestTrade(prices []int) (buy, sell, profit int)
```

- `prices[i]` is the price on day `i`. A trade buys on day `buy` and sells on
  day `sell` with `buy < sell`; its profit is `prices[sell]-prices[buy]`.
- Returns the trade with the largest profit, and that profit, provided the
  profit is positive.
- If no trade has a positive profit (including empty input, a single price,
  and flat or falling prices), returns `(0, 0, 0)`.
- Ties: among trades with the largest profit, choose the smallest `buy`; among
  those, the smallest `sell`.
- Prices may be zero or negative and may repeat. The input must not be
  modified.

## Examples

| prices | result |
|---|---|
| `[7, 1, 5, 3, 6, 4]` | `(1, 4, 5)` |
| `[7, 6, 4, 3, 1]` | `(0, 0, 0)` |
| `[2, 4, 1, 3]` | `(0, 1, 2)` |
| `[1, 5, 1, 5]` | `(0, 1, 4)` |
| `[3, 3, 3]` | `(0, 0, 0)` |
| `[]` | `(0, 0, 0)` |

## Constraints

- `len(prices)` can reach 1,000,000. The call must finish well under a second
  at that size; trying every pair of buy and sell days is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
