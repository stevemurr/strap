# Evaluate formula

The pricing rules engine stores small arithmetic formulas as text and needs to
evaluate them with integer arithmetic. Formulas come from a form that people
type into, so malformed input has to be rejected with an error rather than
guessed at.

## Contract

Package `formulas`, file `evaluate.go`:

```go
func Evaluate(expr string) (int, error)
```

Grammar:

```
expr    := term (('+' | '-') term)*
term    := factor (('*' | '/') factor)*
factor  := '-' factor | '(' expr ')' | integer
integer := one or more ASCII digits
```

- ASCII space bytes (`' '` only; tabs and newlines are not allowed) may
  appear anywhere between tokens. The digits of one literal are never split by
  spaces: `1 2` is malformed.
- Literals are non-negative and may have leading zeros (`007` is 7).
- `*` and `/` bind tighter than `+` and `-`; operators on the same level
  associate left to right, so `8 / 4 / 2` is 1 and `10 - 2 - 3` is 5.
- Unary minus applies to the operand that follows it, which may be a literal,
  a parenthesised expression or another unary minus: `2 * -3` is -6 and
  `--5` is 5. There is no unary plus.
- Division truncates toward zero like Go's `/` on integers: `-7 / 2` is -3.
- Every literal, intermediate value and result fits in `int` (64-bit); the
  hidden tests never overflow.
- Return `(0, err)` with a non-nil error when the input is empty or contains
  only spaces, when it is malformed (unbalanced parentheses, a missing
  operand or operator such as `1 +`, `1 2` or `()`, or any byte outside the
  grammar), or when any division by zero occurs while evaluating.

## Examples

| expr | result |
|---|---|
| `1 + 2 * 3` | 7 |
| `(1 + 2) * 3` | 9 |
| `8 / 4 / 2` | 1 |
| `-7 / 2` | -3 |
| `2 * -3` | -6 |
| `2 - (3 - 4)` | 3 |
| `--5` | 5 |
| ` ` (only spaces) | error |
| `1 +` | error |
| `(1 + 2` | error |
| `4 / (2 - 2)` | error |
| `2 x 3` | error |

## Constraints

- Formulas up to a few megabytes long, with up to 100,000 nested parentheses
  or unary minuses and up to 1,000,000 operands in a row. The call must finish
  in well under a second at that size; repeatedly rewriting the string to
  collapse the innermost parentheses or the leftmost operator is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
