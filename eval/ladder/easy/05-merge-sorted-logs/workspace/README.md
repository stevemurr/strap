# Merge sorted logs

The log viewer shows the output of two services side by side as one timeline.
Each service already delivers its lines in timestamp order, so the viewer only
needs to interleave the two streams.

## Contract

Package `logs`, file `merge.go`:

```go
type Entry struct {
    At   int64 // Unix milliseconds.
    Line string
}

func Merge(a, b []Entry) []Entry
```

- Both inputs are sorted by `At` in non-decreasing order; each may contain
  repeated timestamps.
- The result contains every entry of `a` and every entry of `b`, so its length
  is `len(a)+len(b)`, sorted by `At` in non-decreasing order.
- The merge is stable: when an entry of `a` and an entry of `b` have the same
  `At`, the entry from `a` comes first. Entries with equal `At` from the same
  input keep their original relative order.
- Neither input is modified.
- Timestamps may be negative. When both inputs are empty the result has length
  0; `nil` and an empty slice are both accepted.

## Examples

Entries are written as `At:Line`.

| a | b | result |
|---|---|---|
| `[1:boot, 4:ready]` | `[2:probe, 3:probe]` | `[1:boot, 2:probe, 3:probe, 4:ready]` |
| `[5:a1]` | `[5:b1]` | `[5:a1, 5:b1]` |
| `[1:a1, 1:a2]` | `[0:b1, 1:b2]` | `[0:b1, 1:a1, 1:a2, 1:b2]` |
| `[]` | `[7:x]` | `[7:x]` |
| `[3:x]` | `[]` | `[3:x]` |
| `[]` | `[]` | `[]` |

## Constraints

- Each input can hold 1,000,000 entries. The call must finish well under a
  second at that size; a single pass over both inputs is enough.
- Standard library only. Keep the package name, file name, the `Entry` type and
  the exported signature.
