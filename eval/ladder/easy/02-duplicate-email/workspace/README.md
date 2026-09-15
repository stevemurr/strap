# Duplicate email

The contact importer reads addresses from a customer's spreadsheet in row
order and rejects the file as soon as it sees an address twice. Addresses are
compared after normalizing, because `"Ann@Shop.io "` and `"ann@shop.io"` reach
the same inbox.

## Contract

Package `contacts`, file `duplicate.go`:

```go
func FirstDuplicate(emails []string) (string, bool)
```

- Each address is normalized by removing leading and trailing whitespace
  (`strings.TrimSpace`) and lowercasing (`strings.ToLower`). Interior
  characters are left as they are, so `"a b@x.com"` and `"ab@x.com"` differ.
- Scanning in order, the result is the normalized form of the first address
  whose normalized form already appeared at an earlier position, and `true`.
- When no address repeats, the result is `("", false)`. Empty input and a
  single address never have a duplicate.
- An address that normalizes to the empty string is still an address: if two
  such entries occur, the result is `("", true)`.
- The input slice must not be modified.

## Examples

| emails | result |
|---|---|
| `["ann@shop.io", "bo@shop.io", " Ann@Shop.io "]` | `("ann@shop.io", true)` |
| `["a@x.com", "b@x.com", "b@x.com", "a@x.com"]` | `("b@x.com", true)` |
| `["a@x.com", "b@x.com"]` | `("", false)` |
| `["ann@shop.io", "ann@shop.io"]` | `("ann@shop.io", true)` |
| `["", " "]` | `("", true)` |
| `[]` | `("", false)` |

## Constraints

- `len(emails)` can reach 1,000,000. The call must finish well under a second
  at that size; comparing every address with every other is far too slow.
- Standard library only. Keep the package name, file name and exported signature.
