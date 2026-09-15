# Expand pattern

Test fixtures describe long repetitive payloads with a compact pattern
language instead of checking huge literal strings into the repository: `3[ab]`
stands for `ababab`, and groups nest, so `2[a3[b]]` stands for `abbbabbb`.
Implement the expander that turns such a pattern into its text.

## Contract

Package `fixtures`, file `expand.go`:

```go
func Expand(pattern string) (string, error)
```

Grammar:

```
pattern := item*
item    := letter | count '[' pattern ']'
letter  := 'a'..'z'
count   := '1'..'9' digit*        // decimal, no leading zero, so at least 1
```

- A letter expands to itself; `count[pattern]` expands to the expansion of the
  inner pattern repeated `count` times; items are concatenated in order. The
  empty pattern `""` expands to `""`.
- Groups nest to any depth. The inner pattern of a group must not be empty:
  `3[]` is an error.
- Returns `("", err)` with a non-nil `err` when the pattern is invalid:
  - unbalanced brackets: `"3[ab"`, `"ab]"`, `"]["`;
  - a `[` not immediately preceded by a count: `"[ab]"`, `"a[b]"`;
  - a count not immediately followed by `[`: `"3a"`, `"3"`, `"a3"`;
  - a count of `0` or with a leading zero: `"0[a]"`, `"03[a]"`;
  - an empty group: `"3[]"`;
  - any byte other than a lowercase ASCII letter, a decimal digit, `[` or `]`
    (uppercase letters, spaces, punctuation, non-ASCII bytes);
  - an expansion longer than 10,000,000 bytes. Exactly 10,000,000 is allowed.
    Detect the overflow without building the oversized text:
    `"1000[1000[1000[1000[a]]]]"` must fail quickly without allocating
    gigabytes, and a count with more digits than an `int` holds is an error
    too (its expansion is necessarily too long, since the group is non-empty).

## Examples

| pattern | result |
|---|---|
| `"3[a]2[bc]"` | `"aaabcbc"` |
| `"3[a2[c]]"` | `"accaccacc"` |
| `"2[abc]3[cd]ef"` | `"abcabccdcdcdef"` |
| `"abc"` | `"abc"` |
| `""` | `""` |
| `"10[a]"` | `"aaaaaaaaaa"` |
| `"2[a"` | error |
| `"0[a]"` | error |
| `"3[A]"` | error |

## Constraints

- Patterns up to 10,000 bytes with groups nested up to 100 deep; expansions up
  to 10,000,000 bytes. The hidden tests expand a nested pattern to a million
  bytes and the flat `"1000000[a]"`; each call must finish well under a
  second, so building the output by repeatedly concatenating strings is too
  slow.
- Standard library only. Keep the package name, file name and exported signature.
