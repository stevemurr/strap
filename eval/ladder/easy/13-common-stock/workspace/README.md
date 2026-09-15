# Common stock

Two warehouses each export a flat list of the SKUs on their shelves, one entry
per physical unit, so a SKU with three units on the shelf shows up three times.
The reconciliation job needs the units that can be matched one-for-one between
the two warehouses.

## Contract

Package `inventory`, file `stock.go`:

```go
func CommonStock(a, b []string) []string
```

- The result is the multiset intersection of `a` and `b`: a SKU that appears
  `x` times in `a` and `y` times in `b` appears `min(x, y)` times in the
  result. A SKU present in only one input does not appear at all.
- SKUs are compared exactly, byte for byte; `"a1"` and `"A1"` are different.
- The result is sorted in ascending order using Go's string ordering (`<`),
  which compares bytes.
- When nothing is in common, including when either input is empty, the result
  has length 0. Returning `nil` or an empty non-nil slice are both fine.
- Neither input slice may be modified or reordered.

## Examples

| a | b | result |
|---|---|---|
| `["A1", "B2", "A1"]` | `["A1", "A1", "C3"]` | `["A1", "A1"]` |
| `["X9", "Y1"]` | `["Y1", "X9"]` | `["X9", "Y1"]` |
| `["Z", "Z", "Z"]` | `["Z"]` | `["Z"]` |
| `["A1"]` | `["B2"]` | `[]` |
| `[]` | `["A1"]` | `[]` |
| `["a1"]` | `["A1"]` | `[]` |

## Constraints

- Each input can hold up to 500,000 entries drawn from a few hundred thousand
  distinct SKUs. The call must finish well under a second at that size;
  scanning one list for every entry of the other is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
