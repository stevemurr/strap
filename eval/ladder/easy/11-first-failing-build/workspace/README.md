# First failing build

Builds on the main branch are numbered from 1. At some point a change broke
the nightly job, and every build from that one on fails. The bisection tool
has to find the first failing build by re-running the job on old builds, and
each re-run is expensive, so it must ask about as few builds as possible.

## Contract

Package `bisect`, file `bisect.go`:

```go
func FirstFailing(n int, failing func(build int) bool) int
```

- Builds are numbered `1..n` with `n >= 1`. `failing(b)` reports whether build
  `b` fails. It is monotonic: once a build fails, every later build fails too.
  `failing(n)` is guaranteed to be `true`.
- Returns the smallest build number `b` with `failing(b) == true`.
- `failing` may only be called with arguments in `1..n`.
- `failing` may be called at most `2*ceil(log2(n)) + 2` times per call to
  `FirstFailing`: 2 times when `n == 1`, 8 times when `n == 5` or `n == 8`,
  62 times when `n == 1,000,000,000`. Repeated calls with the same argument
  count separately.
- `failing` is deterministic; no caching is needed.

## Examples

| n | failing builds | result | calls allowed |
|---|---|---|---|
| `1` | `1` | `1` | 2 |
| `5` | `4..5` | `4` | 8 |
| `5` | `1..5` | `1` | 8 |
| `8` | `8` | `8` | 8 |
| `1000000000` | `123456789..1000000000` | `123456789` | 62 |

## Constraints

- `n` can reach 1,000,000,000. The hidden tests count callback invocations and
  fail on any call outside `1..n` or beyond the allowed budget, so a linear
  scan cannot pass.
- Standard library only. Keep the package name, file name and exported signature.
