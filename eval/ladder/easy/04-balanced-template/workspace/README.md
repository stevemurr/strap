# Balanced template

Page templates mix prose with `{{ ... }}` expressions, `[ ... ]` attribute
lists and `( ... )` call arguments. The linter's first check is that those
delimiters open and close in the right order before anything else parses the
file.

## Contract

Package `templates`, file `balanced.go`:

```go
func Balanced(template string) bool
```

- Only the six bytes `(`, `)`, `[`, `]`, `{` and `}` matter; every other byte
  is ignored.
- The template is balanced when every opener is closed by its matching closer,
  pairs are properly nested (an opener that appears later must close earlier),
  and nothing is left open at the end.
- A closer with no open delimiter, or one that does not match the most
  recently opened delimiter, makes the template unbalanced.
- The empty string, and any string without delimiters, is balanced.

## Examples

| template | result |
|---|---|
| `"{{ user.name }}"` | `true` |
| `"[a](b){c}"` | `true` |
| `"{[}]"` | `false` |
| `"((("` | `false` |
| `")("` | `false` |
| `"plain text, no delimiters"` | `true` |
| `""` | `true` |

## Constraints

- Templates can reach 1,000,000 bytes, with nesting as deep as 500,000. The
  call must finish well under a second at that size; repeatedly deleting
  adjacent pairs from the string is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
