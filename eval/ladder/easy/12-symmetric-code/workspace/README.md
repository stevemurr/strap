# Symmetric code

Marketing likes "symmetric" promo codes: ones that read the same forwards and
backwards once punctuation, spaces and letter case are ignored. The code
generator needs a check it can run on every candidate before it is printed on
a flyer.

## Contract

Package `promo`, file `promo.go`:

```go
func IsSymmetric(code string) bool
```

- Only ASCII letters (`A`-`Z`, `a`-`z`) and ASCII digits (`0`-`9`) take part in
  the comparison. Every other byte, including spaces, punctuation and any
  non-ASCII byte, is skipped as if it were not there.
- Letters compare case-insensitively: `A` matches `a`. A digit only matches
  the same digit.
- The code is symmetric when the sequence of counted characters equals its own
  reverse. An empty string, or one with no letters or digits at all, is
  symmetric.
- The string is processed byte by byte; multi-byte characters are never
  decoded.

## Examples

| code | result |
|---|---|
| `"A man, a plan, a canal: Panama"` | `true` |
| `"race a car"` | `false` |
| `"12-21"` | `true` |
| `"0P"` | `false` |
| `"!!!"` | `true` |
| `""` | `true` |

## Constraints

- Codes can be up to 1,000,000 bytes long. The call must finish well under a
  second at that size; a single pass over the bytes is enough, while
  rebuilding the string one character at a time is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
