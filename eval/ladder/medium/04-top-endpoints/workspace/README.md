# Top endpoints

The API analytics page lists the endpoints that received the most traffic in
the selected window. Given the raw access log as a list of endpoint paths, one
entry per request, return the busiest endpoints.

## Contract

Package `traffic`, file `traffic.go`:

```go
func TopEndpoints(hits []string, k int) []string
```

- Returns the `k` distinct endpoints with the highest hit counts, most
  frequent first.
- Endpoints with equal counts are ordered by ascending string comparison, so
  the result is fully determined by the input.
- `k <= 0` returns a zero-length result (nil is fine).
- When `k` exceeds the number of distinct endpoints, every distinct endpoint is
  returned, in the same order (count descending, then name ascending).
- Endpoints are compared byte-wise as strings; `"/a"` and `"/a/"` are
  different endpoints.
- The input slice must not be modified.

## Examples

| hits | k | result |
|---|---|---|
| `["/a", "/b", "/a", "/c", "/b", "/a"]` | 2 | `["/a", "/b"]` |
| `["/p", "/q", "/q", "/r", "/r", "/r"]` | 2 | `["/r", "/q"]` |
| `["/x", "/y", "/y", "/x"]` | 1 | `["/x"]` |
| `["/a", "/b"]` | 5 | `["/a", "/b"]` |
| `["/a"]` | 0 | `[]` |
| `[]` | 3 | `[]` |

## Constraints

- Up to about 1,000,000 hits over up to 50,000 distinct endpoints, with `k` up
  to 100. The call must finish well under a second at that size; counting an
  endpoint by rescanning the whole log is too slow.
- Standard library only. Keep the package name, file name and exported signature.
