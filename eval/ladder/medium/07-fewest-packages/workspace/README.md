# Fewest packages

The warehouse ships an item in fixed package sizes and has unlimited stock of
every size. Given the available sizes and the quantity a customer ordered,
find the smallest number of packages that adds up to the order exactly.

## Contract

Package `packing`, file `packing.go`:

```go
func FewestPackages(sizes []int, quantity int) int
```

- Every size is at least 1, and the same size may be used any number of
  times. Sizes may repeat and are in no particular order.
- Returns the smallest count of packages whose sizes sum to exactly
  `quantity`.
- Returns `-1` when no combination sums to `quantity`, including when `sizes`
  is empty and `quantity > 0`.
- `quantity == 0` returns 0 (nothing to ship), whatever the sizes.
- A negative `quantity` returns `-1`.
- The input slice must not be modified.

## Examples

| sizes | quantity | result |
|---|---|---|
| `[1, 2, 5]` | 11 | 3 |
| `[1, 3, 4]` | 6 | 2 |
| `[2]` | 3 | -1 |
| `[3, 7]` | 5 | -1 |
| `[5, 10]` | 0 | 0 |
| `[4, 6]` | -2 | -1 |
| `[]` | 5 | -1 |

## Constraints

- Quantities up to 100,000 with up to 50 sizes, including orders that cannot
  be filled at all. The call must finish well under a second at that size;
  trying combinations recursively without remembering sub-results is far too
  slow, and always taking the largest size first gives wrong answers.
- Standard library only. Keep the package name, file name and exported signature.
