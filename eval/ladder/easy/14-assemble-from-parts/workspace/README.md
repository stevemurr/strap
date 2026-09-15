# Assemble from parts

The kitting station receives the bill of parts for one unit of a product and
the list of loose parts currently in the bin, one entry per physical part. It
needs to know whether the unit can be assembled from the bin as it stands
before it commits a picker.

## Contract

Package `assembly`, file `assembly.go`:

```go
func CanAssemble(needed, available []string) bool
```

- Returns `true` when every entry in `needed` can be matched to a distinct
  entry in `available` with the same name. A part name that appears `k` times
  in `needed` therefore has to appear at least `k` times in `available`.
- Part names are compared exactly, byte for byte; `"Bolt"` and `"bolt"` are
  different parts.
- Extra parts in `available` are fine. An empty `needed` list is always
  assemblable, even from an empty bin.
- Neither input slice may be modified or reordered.

## Examples

| needed | available | result |
|---|---|---|
| `["bolt", "bolt", "nut"]` | `["nut", "bolt", "washer", "bolt"]` | `true` |
| `["bolt", "bolt"]` | `["bolt", "nut"]` | `false` |
| `["gear"]` | `[]` | `false` |
| `[]` | `[]` | `true` |
| `["Bolt"]` | `["bolt"]` | `false` |

## Constraints

- Both lists can hold up to 1,000,000 entries. The call must finish well under
  a second at that size; searching the bin from the start for every needed
  part is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
