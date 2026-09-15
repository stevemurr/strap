# Running median

The latency dashboard shows the median response time of every request seen so
far and refreshes it after each request arrives. Implement a tracker that
accepts samples one at a time and reports the current median on demand, fast
enough to be queried after every single sample.

## Contract

Package `latency`, file `tracker.go`:

```go
type Tracker struct { /* ... */ }

func New() *Tracker
func (t *Tracker) Add(sample int)
func (t *Tracker) Median() float64
```

- `New` returns an empty tracker.
- `Add` records one sample. Samples may be negative and may repeat, and every
  sample stays in the tracker forever.
- `Median` returns the median of all samples added so far: with an odd count,
  the middle value of the sorted samples; with an even count, the mean of the
  two middle values. It returns `0` when no sample has been added.
- `Add` must run in logarithmic time and `Median` in constant time; the tests
  call `Median` after every `Add`.
- The tracker is used from a single goroutine; no locking is needed.

## Examples

| operation | Median() afterwards |
|---|---|
| `New()` | 0 |
| `Add(5)` | 5 |
| `Add(2)` | 3.5 |
| `Add(9)` | 5 |
| `Add(-4)` | 3.5 |
| `Add(2)` | 2 |

## Constraints

- Up to 1,000,000 samples with values between -1,000,000 and 1,000,000 and a
  `Median` call after every `Add`. The whole sequence must finish well under a
  second; inserting into a sorted slice, or sorting when `Median` is called, is
  too slow.
- Standard library only. Keep the package name, file name and exported API.
