# Longest balanced span

The template linter highlights a file's longest stretch of correctly nested
delimiters so an author can see where balance breaks down. The linter has
already reduced a template to its bracket markers; you receive that string.

## Contract

Package `markers`, file `balanced.go`:

```go
func LongestBalanced(markers string) int
```

- `markers` contains only the bytes `'('` and `')'`; the hidden tests never
  pass anything else.
- A string is well-formed when it is empty, or it is `(` followed by a
  well-formed string followed by `)`, or it is two well-formed strings joined
  together. Return the length of the longest contiguous substring of `markers`
  that is well-formed.
- Empty input returns 0, as does any input with no well-formed substring. The
  result is always even.

## Examples

| markers | result |
|---|---|
| `"(()"` | 2 |
| `")()())"` | 4 |
| `"()(())"` | 6 |
| `"()(()"` | 2 |
| `"(((("` | 0 |
| `""` | 0 |

## Constraints

- Up to 5,000,000 bytes. The call must finish in well under a second at that
  size; checking every substring, or rescanning forward from every start
  position, is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
