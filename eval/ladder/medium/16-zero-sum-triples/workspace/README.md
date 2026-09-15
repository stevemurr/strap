# Zero-sum triples

Reconciliation flags groups of three ledger deltas that cancel out exactly, so
an analyst can review them together. Given the deltas, report every distinct
combination of three values that sums to zero.

## Contract

Package `reconcile`, file `triples.go`:

```go
func ZeroSumTriples(deltas []int) [][3]int
```

- A qualifying triple is `[a, b, c]` with `a <= b <= c`, `a+b+c == 0`, and the
  three values taken from three different positions of `deltas`. Equal values
  at different positions may be combined: `[0, 0, 0]` qualifies when `0`
  occurs at least three times, `[-1, -1, 2]` when `-1` occurs at least twice
  and `2` at least once.
- Each distinct value triple is reported exactly once, however many position
  combinations produce it.
- The result is sorted ascending by `a`, then by `b` (which fixes `c`).
- Returns a zero-length slice (`nil` is fine) when no triple qualifies,
  including for fewer than three deltas.
- The input must not be modified.

## Examples

| deltas | result |
|---|---|
| `[-1, 0, 1, 2, -1, -4]` | `[[-1, -1, 2], [-1, 0, 1]]` |
| `[0, 0, 0, 0]` | `[[0, 0, 0]]` |
| `[0, 0]` | `[]` |
| `[3, -2, -1, -2, 4]` | `[[-2, -2, 4], [-2, -1, 3]]` |
| `[1, 2, 3]` | `[]` |
| `[-5, 5, 0, 5, -5]` | `[[-5, 0, 5]]` |

## Constraints

- Up to 6,000 deltas with values in `[-1000000, 1000000]`. The hidden tests
  include 6,000 values drawn from `[-1000, 1000]` (heavy duplication, several
  hundred thousand distinct triples) and 6,000 values drawn from the full
  range. Each call must finish within a second; trying every combination of
  three positions is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
