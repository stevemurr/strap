# Exclusive products

The pricing engine models a bundle discount as a chain of multiplicative
factors, one per item. To show what a bundle would cost without any single
item, it needs, for every position, the product of all the other factors.

## Contract

Package `pricing`, file `pricing.go`:

```go
func ExclusiveProducts(factors []int) []int
```

- Returns a new slice `out` of the same length where `out[i]` is the product
  of every factor except `factors[i]`.
- Zero is a valid factor. With exactly one zero, every position except the
  zero's gets 0 and the zero's position gets the product of the rest; with two
  or more zeros every position is 0.
- Factors may be negative. The product of the absolute values of all the
  non-zero factors always fits in a signed 64-bit integer, so every result
  fits as well.
- Fewer than two factors returns `nil`.
- The input slice must not be modified.

## Examples

| factors | result |
|---|---|
| `[1, 2, 3, 4]` | `[24, 12, 8, 6]` |
| `[2, 0, 5]` | `[0, 10, 0]` |
| `[0, 3, 0]` | `[0, 0, 0]` |
| `[-2, 3]` | `[3, -2]` |
| `[1, -1, 1, -1]` | `[1, -1, 1, -1]` |
| `[7]` | `nil` |
| `[]` | `nil` |

## Constraints

- Up to 1,000,000 factors, mostly `1` and `-1` with a few zeros. The call must
  finish well under a second at that size; multiplying the other factors from
  scratch for every position is too slow.
- Standard library only. Keep the package name, file name and exported signature.
