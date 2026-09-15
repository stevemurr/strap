# Merge feeds

The activity service pulls one timestamp-sorted feed per followed account and
has to present a single timeline. Given the feeds, each already sorted in
ascending order, produce one ascending slice holding every entry.

## Contract

Package `feeds`, file `merge.go`:

```go
func Merge(streams [][]int) []int
```

- Every stream is sorted in non-decreasing order. Values may be negative and
  may repeat, both within a stream and across streams.
- The result contains every element of every stream, so its length is the sum
  of the stream lengths, and it is sorted in non-decreasing order.
- No streams at all, or streams that are all empty (nil or zero-length), give
  an empty result: `len == 0`, and `nil` is acceptable.
- The result is a freshly allocated slice that shares no memory with any
  input stream, and the input streams must not be modified.

## Examples

| streams | result |
|---|---|
| `[[1, 4, 5], [1, 3, 4], [2, 6]]` | `[1, 1, 2, 3, 4, 4, 5, 6]` |
| `[[-3, -1], [-2, 0], []]` | `[-3, -2, -1, 0]` |
| `[[7]]` | `[7]` |
| `[[1, 1], [1]]` | `[1, 1, 1]` |
| `[[], []]` | `[]` |
| `[]` | `[]` |

## Constraints

- Up to 20,000 streams of up to 50 elements each, 1,000,000 elements in total.
  The call must finish in well under a second at that size; choosing each
  output element by looking at the head of every stream is far too slow, and
  so is merging the streams one after another into a growing result.
- Standard library only. Keep the package name, file name and exported signature.
