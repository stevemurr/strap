# Runs with total

The reconciliation job looks for stretches of consecutive ledger entries that
net out to a given amount, for example a refund that cancels a run of charges.
Given the entry amounts in ledger order and a target, count how many
contiguous runs sum to exactly the target.

## Contract

Package `ledger`, file `runs.go`:

```go
func RunsWithTotal(amounts []int, target int) int
```

- A run is a contiguous, non-empty slice `amounts[i:j]` with `0 <= i < j <=
  len(amounts)`. Two runs are different when their `(i, j)` differ, even if
  they hold the same values.
- Returns the number of runs whose amounts sum to exactly `target`.
- Amounts and the target may be zero or negative. An empty ledger has no runs,
  so the result is `0`.
- The count can be as large as `n(n+1)/2`; it fits in an `int`, as do all
  partial sums.
- The input must not be modified.

## Examples

| amounts | target | result |
|---|---|---|
| `[1, 1, 1]` | 2 | `2` |
| `[1, 2, 3]` | 3 | `2` |
| `[3, -1, 4, -1, 5]` | 3 | `3` |
| `[0, 0, 0]` | 0 | `6` |
| `[-2, 2, -2, 2]` | 0 | `4` |
| `[5]` | 5 | `1` |
| `[]` | 0 | `0` |

## Constraints

- Up to 300,000 entries. The hidden tests include 300,000 zeros with target
  0, whose answer is 45,000,150,000; the call must finish well under a second,
  so enumerating every `(i, j)` pair is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
