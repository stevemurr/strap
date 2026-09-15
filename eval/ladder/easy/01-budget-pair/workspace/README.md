# Budget pair

The gift-card checkout lets a shopper spend a card down to zero by picking two
different catalog items. Given the catalog prices in display order and the card
balance, find a pair of items whose prices add up to the balance exactly.

## Contract

Package `budgetpair`, file `budget.go`:

```go
func PairForBudget(prices []int, budget int) (i, j int, ok bool)
```

- Returns indexes `i < j` of two distinct items with `prices[i]+prices[j] == budget`
  and `ok == true`.
- When several pairs qualify, return the pair whose second index `j` is
  smallest; if the same `j` pairs with several `i`, return the smallest `i`.
- Returns `(0, 0, false)` when no pair qualifies. Catalogs with fewer than two
  items never qualify.
- Prices may be zero or negative (store credits) and may repeat. An item cannot
  be paired with itself, but two different items with the same price can pair.

## Examples

| prices | budget | result |
|---|---|---|
| `[25, 40, 15, 60]` | 55 | `(1, 2, true)` |
| `[3, 3]` | 6 | `(0, 1, true)` |
| `[4, 6, 4, 6, 4]` | 10 | `(0, 1, true)` |
| `[1, 2, 3]` | 7 | `(0, 0, false)` |
| `[]` | 0 | `(0, 0, false)` |

## Constraints

- `len(prices)` can reach 1,000,000. The call must finish well under a second
  at that size; checking every pair is too slow.
- Standard library only. Keep the package name, file name and exported signature.
