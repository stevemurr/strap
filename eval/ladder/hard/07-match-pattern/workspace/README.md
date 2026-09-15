# Match pattern

The artifact store lets a deploy manifest select files with glob-like
patterns. Only two wildcards exist, and a pattern has to account for the whole
file name rather than a substring of it.

## Contract

Package `globs`, file `match.go`:

```go
func Match(pattern, name string) bool
```

- `?` matches exactly one byte of `name`, whatever it is.
- `*` matches any sequence of bytes, including the empty sequence. Adjacent
  stars behave like a single star.
- Every other byte in `pattern` matches only the identical byte in `name`.
  Matching is byte-wise and case-sensitive; there is no escaping, there are no
  character classes, and `/` and `.` are ordinary bytes.
- The whole of `name` must be matched by the whole of `pattern`. An empty
  pattern matches only an empty name; a pattern made only of stars matches
  every name.

## Examples

| pattern | name | result |
|---|---|---|
| `*.log` | `app.log` | true |
| `release-?.?.?` | `release-1.2.3` | true |
| `a?c` | `ac` | false |
| `a*b` | `acbd` | false |
| `*` | `` (empty) | true |
| `**a` | `a` | true |

## Constraints

- Names up to 30,000 bytes and patterns up to a few hundred bytes with dozens
  of stars. Every call, including ones that end in `false`, must finish in
  well under a second; trying every way of splitting the name among the stars
  takes exponential time and is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
