# Rolling peak

The metrics dashboard draws a "peak over the last k samples" line for every
sensor. Given a sensor's readings in time order and the window size k, produce
the peak of every window of k consecutive readings.

## Contract

Package `rollingpeak`, file `peak.go`:

```go
func PeakPerWindow(readings []int, k int) []int
```

- Window `w` covers `readings[w : w+k]`; the result holds one peak per window,
  in order of the window's start, so its length is `len(readings)-k+1`.
- Returns `nil` when `k < 1` or `k > len(readings)`.
- Readings may be negative and may repeat.
- The input must not be modified.

## Examples

| readings | k | result |
|---|---|---|
| `[1, 3, -1, -3, 5, 3, 6, 7]` | 3 | `[3, 3, 5, 5, 6, 7]` |
| `[4, 4, 4]` | 2 | `[4, 4]` |
| `[9, 8, 7]` | 1 | `[9, 8, 7]` |
| `[9, 8, 7]` | 3 | `[9]` |
| `[1, 2]` | 3 | `nil` |

## Constraints

- Up to 1,000,000 readings with k up to 100,000. The call must finish in well
  under a second at that size; recomputing the maximum of each window from
  scratch is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
