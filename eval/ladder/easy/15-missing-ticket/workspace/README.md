# Missing ticket

The support desk hands out ticket numbers 0, 1, 2, ... in order and logs each
one as it is issued. At the end of the day the log for a batch of n+1 tickets
has only n entries, in whatever order the agents wrote them down, and exactly
one number is missing. Find it.

## Contract

Package `tickets`, file `tickets.go`:

```go
func Missing(issued []int) int
```

- Let `n = len(issued)`. The input contains every integer from `0` to `n`
  inclusive except exactly one, each at most once, in any order. Return the
  one that is missing.
- The missing number can be `0` or `n` itself.
- An empty input returns `0`: the batch consisted of the single ticket `0`,
  and it is the one that is missing.
- The input must not be modified or reordered.
- Inputs that break the rule above (duplicates, values outside `0..n`) never
  occur; behaviour for them is unspecified.

## Examples

| issued | result |
|---|---|
| `[3, 0, 1]` | `2` |
| `[0, 1]` | `2` |
| `[1]` | `0` |
| `[9, 6, 4, 2, 3, 5, 7, 0, 1]` | `8` |
| `[]` | `0` |

## Constraints

- Up to 1,000,000 entries. The call must finish well under a second at that
  size; checking each candidate number against the whole log is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
