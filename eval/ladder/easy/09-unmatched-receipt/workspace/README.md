# Unmatched receipt

Every card payment produces two receipt records with the same id: one from the
terminal and one from the acquirer. The nightly reconciliation job collects the
ids from both feeds and, when exactly one payment is missing its counterpart,
needs to know which id it is.

## Contract

Package `receipts`, file `unmatched.go`:

```go
func Unmatched(ids []int) int
```

- `ids` holds every receipt id from both feeds. Every id appears exactly twice
  except for one id, which appears exactly once.
- Returns the id that appears once.
- Ids may be negative or zero; the two copies of an id are not necessarily
  adjacent.
- `len(ids)` is always odd and at least 1. A single-element input returns that
  element.
- The input must not be modified.

## Examples

| ids | result |
|---|---|
| `[4, 1, 2, 1, 2]` | `4` |
| `[2, 2, 1]` | `1` |
| `[7]` | `7` |
| `[-3, 5, 5]` | `-3` |
| `[0, 9, 0]` | `9` |
| `[1, 2, 3, 2, 1]` | `3` |

## Constraints

- `len(ids)` can reach 1,000,001. The call must finish well under a second at
  that size; counting each id by rescanning the whole slice is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
