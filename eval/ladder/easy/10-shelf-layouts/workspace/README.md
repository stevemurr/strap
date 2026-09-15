# Shelf layouts

The warehouse planner fills a shelf from left to right with boxes that are
either 1 unit or 2 units wide, with no gaps. The planner reports how many
distinct layouts exist for a shelf of a given width; two layouts are different
when their left-to-right sequences of box widths differ.

## Contract

Package `shelving`, file `layouts.go`:

```go
func Layouts(width int) int64
```

- Returns the number of distinct sequences of 1-unit and 2-unit boxes whose
  widths add up to exactly `width`.
- `Layouts(0)` is `1` (the empty layout). `Layouts(1)` is `1` and
  `Layouts(2)` is `2` (`1+1` and `2`).
- For every `width >= 2`, `Layouts(width) == Layouts(width-1) + Layouts(width-2)`.
- A negative `width` returns `0`.
- Callers pass widths of at most 90; `Layouts(90)` is `4660046610375530309`,
  which fits in an `int64`. Behaviour above 90 is unspecified.

## Examples

| width | result |
|---|---|
| `-1` | `0` |
| `0` | `1` |
| `1` | `1` |
| `2` | `2` |
| `3` | `3` |
| `4` | `5` |
| `10` | `89` |
| `90` | `4660046610375530309` |

## Constraints

- The hidden tests check every width from -5 to 90 and expect the whole range
  to finish well under a second; plain recursion without memoization is far too
  slow at width 90.
- Standard library only. Keep the package name, file name and exported signature.
