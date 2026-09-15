# Compact slots

A shift roster is a fixed-length list of slots, one per position, holding the
assigned employee's id or `""` when the position is unfilled. Before the
roster is displayed, the filled slots are pulled to the front so the gaps sit
together at the end.

## Contract

Package `slots`, file `compact.go`:

```go
func Compact(slots []string)
```

- Rearranges `slots` in place so that every non-empty string comes before
  every `""`, with the non-empty strings keeping their relative order.
- The number of `""` entries is unchanged, so the slice keeps its length; the
  caller sees the result through the slice it passed in. Only the elements of
  `slots` may be written, never anything beyond `len(slots)`.
- Only the exact empty string `""` counts as empty. `" "` and other whitespace
  strings are ordinary values and keep their place among the non-empty
  strings.
- Duplicates are allowed. A `nil` or empty slice, and a slice with no `""`, are
  left as they are.

## Examples

| slots before | slots after |
|---|---|
| `["ann", "", "bo", "", "cy"]` | `["ann", "bo", "cy", "", ""]` |
| `["", "", "di"]` | `["di", "", ""]` |
| `["", " ", ""]` | `[" ", "", ""]` |
| `["", ""]` | `["", ""]` |
| `["ann", "bo"]` | `["ann", "bo"]` |
| `[]` | `[]` |

## Constraints

- `len(slots)` can reach 1,000,000 with half of the slots empty. The call must
  finish well under a second at that size; shifting the tail left once per
  empty slot is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
