# Same typed

The collaborative editor records every keystroke in a text field as a log:
one lowercase ASCII letter per typed character and `#` for each press of
backspace. Two clients that end up with the same text should be treated as in
sync even if they got there differently, so two logs are compared by the text
they produce.

## Contract

Package `editor`, file `editor.go`:

```go
func SameTyped(a, b string) bool
```

- Each log is replayed from the start. A lowercase letter (`a`-`z`) appends
  itself to the text. A `#` deletes the most recently typed character that
  has not already been deleted; when the text is empty, `#` does nothing.
- Returns `true` when replaying `a` and replaying `b` produce the same final
  text, and `false` otherwise.
- Logs contain only lowercase ASCII letters and `#`; other bytes never occur.
- An empty log produces the empty text, so `SameTyped("", "")` and
  `SameTyped("###", "")` are both `true`.

## Examples

| a | b | result |
|---|---|---|
| `"ab#c"` | `"ad#c"` | `true` |
| `"ab##"` | `"c#d#"` | `true` |
| `"a#c"` | `"b"` | `false` |
| `"a##b"` | `"#b"` | `true` |
| `"abc"` | `"abc#"` | `false` |
| `""` | `""` | `true` |

## Constraints

- Each log can be up to 1,000,000 bytes. The call must finish well under a
  second at that size; one pass over each log is enough, while growing the
  text as a string one keystroke at a time is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
