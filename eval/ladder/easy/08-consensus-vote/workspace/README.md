# Consensus vote

When replicas disagree about a record, the reconciler accepts a value only if
more than half of the replicas reported it. Anything short of a strict
majority is treated as no consensus and escalated.

## Contract

Package `voting`, file `consensus.go`:

```go
func Consensus(votes []string) (string, bool)
```

- Returns the value that appears more than `len(votes)/2` times (strictly more
  than half of the votes) and `true`.
- If no value reaches that count, returns `("", false)`. Empty input has no
  consensus, and exactly half of the votes is not a majority.
- Votes are compared exactly and case-sensitively: `"Yes"` and `"yes"` are
  different values.
- `""` is an ordinary vote value: when it holds the majority the result is
  `("", true)`.
- The input must not be modified.

## Examples

| votes | result |
|---|---|
| `["yes", "no", "yes"]` | `("yes", true)` |
| `["a", "b", "c", "a"]` | `("", false)` |
| `["x"]` | `("x", true)` |
| `["a", "a", "b", "b", "b"]` | `("b", true)` |
| `["yes", "Yes", "yes", "no", "no"]` | `("", false)` |
| `[]` | `("", false)` |

## Constraints

- `len(votes)` can reach 1,000,000, with up to 1,000,000 distinct values. The
  call must finish well under a second at that size; counting each value by
  rescanning the whole slice is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
