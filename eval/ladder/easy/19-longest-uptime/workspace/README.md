# Longest uptime

The status page polls a service once a minute and stores each result as a
bool: `true` when the health check passed, `false` when it did not. The
monthly report shows the longest unbroken stretch of passing checks.

## Contract

Package `uptime`, file `uptime.go`:

```go
func Longest(up []bool) int
```

- Returns the length of the longest run of consecutive `true` values in `up`.
- Runs are separated by `false` values; a run may start at index 0 or end at
  the last index.
- An empty input, or one with no `true` values, returns `0`.
- The input must not be modified.

## Examples

| up | result |
|---|---|
| `[true, true, false, true, true, true]` | `3` |
| `[true, false, true]` | `1` |
| `[true, true, true, true]` | `4` |
| `[false, false]` | `0` |
| `[true]` | `1` |
| `[]` | `0` |

## Constraints

- Up to 10,000,000 samples. The call must finish well under a second at that
  size; a single pass is enough, and anything that rescans the slice for each
  sample is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
