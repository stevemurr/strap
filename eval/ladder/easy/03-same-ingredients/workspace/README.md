# Same ingredients

The recipe service merges submissions that call for exactly the same shopping
list. Two ingredient lists match when they contain the same names the same
number of times, whatever order the cook wrote them in.

## Contract

Package `recipes`, file `ingredients.go`:

```go
func SameIngredients(a, b []string) bool
```

- Returns `true` when `a` and `b` contain the same strings with the same
  multiplicities, ignoring order; otherwise `false`.
- Names are compared exactly and case-sensitively: `"Egg"` and `"egg"` are
  different ingredients, and so are `"egg"` and `"egg "`. The empty string is
  an ordinary name.
- Lists of different lengths never match.
- `nil` and an empty list are equal to each other; neither matches a
  non-empty list.
- Neither input is modified.

## Examples

| a | b | result |
|---|---|---|
| `["flour", "egg", "milk"]` | `["milk", "flour", "egg"]` | `true` |
| `["egg", "egg", "milk"]` | `["egg", "milk", "milk"]` | `false` |
| `["Egg"]` | `["egg"]` | `false` |
| `["salt"]` | `["salt", "salt"]` | `false` |
| `["salt", "salt"]` | `["salt", "salt"]` | `true` |
| `[]` | `nil` | `true` |

## Constraints

- Each list can hold 1,000,000 names, all of them distinct in the worst case.
  The call must finish in a couple of seconds at most at that size; counting
  a name by rescanning the other list for every entry is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
